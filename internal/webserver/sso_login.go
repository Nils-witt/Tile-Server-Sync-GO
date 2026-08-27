// This file implements the OpenID Connect authorization-code flow: a
// two-step redirect dance (start, then callback) run entirely from the
// currently stored configdb.SSOConfig. Unlike runtime.reload's config, this
// deliberately isn't cached anywhere in the process — SSO logins are
// infrequent enough (interactive, human-driven) that resolving the
// provider's discovery document fresh on each attempt is cheap, and it
// avoids adding a second live-reload path to maintain alongside the
// tileserve/database one in runtime.go.

package webserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go-sync-objects/internal/configdb"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// ssoFlowCookieName holds the short-lived, per-attempt OIDC flow state (see
// ssoFlowState) between /login/sso and /login/sso/callback. It is never
// meant to outlive a single login attempt.
const ssoFlowCookieName = "gso_sso_flow"

// ssoFlowTTL bounds how long a user has to complete the provider's login
// screen before the flow cookie expires and the callback is rejected.
const ssoFlowTTL = 5 * time.Minute

// oidcTimeout bounds each network round-trip to the OIDC provider
// (discovery, token exchange) so an unreachable or slow IdP fails the login
// attempt instead of hanging the request indefinitely.
const oidcTimeout = 10 * time.Second

// ssoFlowState is the JSON payload stored in ssoFlowCookieName across the
// redirect to the provider and back.
type ssoFlowState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
}

// loginSSOStartHandler serves GET /login/sso?next=...: builds the provider
// redirect and sends the browser there. Unauthenticated, like /login itself.
func loginSSOStartHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		ssoCfg, err := cfgDB.LoadSSOConfig(r.Context())
		if err != nil || !ssoCfg.Enabled {
			http.NotFound(w, r)
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), oidcTimeout)
		defer cancel()

		oauth2Cfg, _, err := buildOAuth2Config(ctx, ssoCfg, r)
		if err != nil {
			log.Printf("sso: start login: %v", err)
			redirectSSOError(w, r)

			return
		}

		state, nonce, verifier, err := newSSOFlowState()
		if err != nil {
			log.Printf("sso: start login: %v", err)
			redirectSSOError(w, r)

			return
		}

		flow := ssoFlowState{State: state, Nonce: nonce, Verifier: verifier, Next: safeNext(r.URL.Query().Get("next"))}
		if err := setSSOFlowCookie(w, r, flow); err != nil {
			log.Printf("sso: start login: %v", err)
			redirectSSOError(w, r)

			return
		}

		authURL := oauth2Cfg.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
		http.Redirect(w, r, authURL, http.StatusSeeOther)
	}
}

// loginSSOCallbackHandler serves GET /login/sso/callback: verifies the
// provider's response, resolves it to a local user (auto-provisioning one if
// needed — see configdb.Store.FindOrCreateSSOUser), and starts a session
// exactly like handleLoginPost does for a password login.
func loginSSOCallbackHandler(cfgDB *configdb.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		flow, err := readAndClearSSOFlowCookie(w, r)
		if err != nil {
			log.Printf("sso: callback: %v", err)
			logSecurityEvent(r, cfgDB, "sso_login_failed", "", err.Error())
			redirectSSOError(w, r)

			return
		}

		if r.URL.Query().Get("state") != flow.State {
			log.Print("sso: callback: state mismatch")
			logSecurityEvent(r, cfgDB, "sso_login_failed", "", "state mismatch")
			redirectSSOError(w, r)

			return
		}

		user, err := completeSSOLogin(r, cfgDB, flow)
		if err != nil {
			log.Printf("sso: callback: %v", err)
			logSecurityEvent(r, cfgDB, "sso_login_failed", "", err.Error())
			redirectSSOError(w, r)

			return
		}

		token, expiresAt, err := cfgDB.CreateSession(r.Context(), user.ID, sessionTTL)
		if err != nil {
			log.Printf("sso: callback: create session: %v", err)
			logSecurityEvent(r, cfgDB, "sso_login_failed", user.Username, err.Error())
			redirectSSOError(w, r)

			return
		}

		logSecurityEvent(r, cfgDB, "sso_login", user.Username, "")
		setSessionCookie(w, r, token, expiresAt)
		http.Redirect(w, r, safeNext(flow.Next), http.StatusSeeOther)
	}
}

// completeSSOLogin does the actual provider round-trip once the callback's
// state has already been checked against the flow cookie: rebuild the same
// oauth2/OIDC configuration used to start the flow, exchange the
// authorization code, verify the ID token (including the nonce from the
// flow cookie), and resolve the verified identity to a local user.
func completeSSOLogin(r *http.Request, cfgDB *configdb.Store, flow ssoFlowState) (*configdb.User, error) {
	ssoCfg, err := cfgDB.LoadSSOConfig(r.Context())
	if err != nil {
		return nil, fmt.Errorf("load sso config: %w", err)
	}

	if !ssoCfg.Enabled {
		return nil, errors.New("sso is not enabled")
	}

	ctx, cancel := context.WithTimeout(r.Context(), oidcTimeout)
	defer cancel()

	oauth2Cfg, provider, err := buildOAuth2Config(ctx, ssoCfg, r)
	if err != nil {
		return nil, err
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, errors.New("missing authorization code")
	}

	token, err := oauth2Cfg.Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("token response has no id_token")
	}

	verifier := provider.Verifier(&oidc.Config{ClientID: ssoCfg.ClientID})

	idToken, err := verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}

	if idToken.Nonce != flow.Nonce {
		return nil, errors.New("nonce mismatch")
	}

	var claims struct {
		Subject           string `json:"sub"`
		Email             string `json:"email"`
		PreferredUsername string `json:"preferred_username"`
	}

	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	username := usernameFromClaims(claims.Email, claims.PreferredUsername, idToken.Subject)

	user, err := cfgDB.FindOrCreateSSOUser(r.Context(), idToken.Issuer, idToken.Subject, username, ssoCfg.DefaultPermissions)
	if err != nil {
		return nil, fmt.Errorf("resolve sso user: %w", err)
	}

	return user, nil
}

// usernameFromClaims picks the local username a newly provisioned SSO
// account is created with (see configdb.Store.FindOrCreateSSOUser step 2:
// this is also what an admin should pre-create a placeholder account as, to
// have it linked instead of auto-provisioned): the email claim if present,
// else preferred_username, else the subject identifier itself.
func usernameFromClaims(email, preferredUsername, subject string) string {
	switch {
	case email != "":
		return email
	case preferredUsername != "":
		return preferredUsername
	default:
		return subject
	}
}

// buildOAuth2Config resolves the OIDC provider's discovery document and
// assembles the oauth2.Config used for both the initial redirect and the
// callback's code exchange. Both call sites must derive the exact same
// RedirectURL, since providers require it to match byte-for-byte between the
// authorization request and the token exchange.
func buildOAuth2Config(
	ctx context.Context, ssoCfg *configdb.SSOConfig, r *http.Request,
) (*oauth2.Config, *oidc.Provider, error) {
	provider, err := oidc.NewProvider(ctx, ssoCfg.IssuerURL)
	if err != nil {
		return nil, nil, fmt.Errorf("discover oidc provider: %w", err)
	}

	scopes := strings.Fields(ssoCfg.Scopes)
	if len(scopes) == 0 {
		scopes = strings.Fields(defaultSSOScopes)
	}

	return &oauth2.Config{
		ClientID:     ssoCfg.ClientID,
		ClientSecret: ssoCfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  ssoRedirectURL(ssoCfg, r),
		Scopes:       scopes,
	}, provider, nil
}

// ssoRedirectURL returns the configured RedirectBaseURL (for a deployment
// behind a reverse proxy/TLS terminator, where the request this process
// sees doesn't reflect the externally visible URL) plus "/login/sso/callback",
// or one derived from the incoming request's own scheme/host when
// RedirectBaseURL is left blank.
func ssoRedirectURL(ssoCfg *configdb.SSOConfig, r *http.Request) string {
	base := strings.TrimSuffix(ssoCfg.RedirectBaseURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		base = scheme + "://" + r.Host
	}

	return base + "/login/sso/callback"
}

// newSSOFlowState generates the random state, nonce, and PKCE verifier for
// one login attempt.
func newSSOFlowState() (state, nonce, verifier string, err error) {
	state, err = randomURLSafeString()
	if err != nil {
		return "", "", "", fmt.Errorf("generate state: %w", err)
	}

	nonce, err = randomURLSafeString()
	if err != nil {
		return "", "", "", fmt.Errorf("generate nonce: %w", err)
	}

	return state, nonce, oauth2.GenerateVerifier(), nil
}

// randomURLSafeString generates a random, URL-safe token (32 bytes of
// crypto/rand, base64-encoded) — the same construction CreateSession in
// configdb/users.go uses for session tokens.
func randomURLSafeString() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// setSSOFlowCookie stores flow as base64url-encoded JSON in a short-lived,
// HttpOnly, SameSite=Lax cookie — the same Secure-on-r.TLS reasoning as
// setSessionCookie in auth.go. The JSON is base64-encoded because a raw
// JSON payload contains characters (`"`, `{`, `:`, ...) that RFC 6265
// forbids in a cookie-value; net/http silently drops such bytes when
// parsing the cookie back out of a later request, corrupting the JSON.
func setSSOFlowCookie(w http.ResponseWriter, r *http.Request, flow ssoFlowState) error {
	data, err := json.Marshal(flow)
	if err != nil {
		return fmt.Errorf("encode sso flow state: %w", err)
	}

	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is conditional on r.TLS, see setSessionCookie
		Name:     ssoFlowCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(data),
		Path:     "/login/sso",
		Expires:  time.Now().Add(ssoFlowTTL),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})

	return nil
}

// readAndClearSSOFlowCookie reads back the flow cookie set by
// setSSOFlowCookie and immediately clears it — a callback can only ever be
// completed once per /login/sso redirect.
func readAndClearSSOFlowCookie(w http.ResponseWriter, r *http.Request) (ssoFlowState, error) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure is conditional on r.TLS, see setSessionCookie
		Name:     ssoFlowCookieName,
		Value:    "",
		Path:     "/login/sso",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})

	cookie, err := r.Cookie(ssoFlowCookieName)
	if err != nil {
		return ssoFlowState{}, errors.New("missing or expired sso flow cookie")
	}

	data, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ssoFlowState{}, fmt.Errorf("decode sso flow cookie: %w", err)
	}

	var flow ssoFlowState
	if err := json.Unmarshal(data, &flow); err != nil {
		return ssoFlowState{}, fmt.Errorf("decode sso flow cookie: %w", err)
	}

	return flow, nil
}

// redirectSSOError sends the browser back to /login with a generic error
// flag; the specific failure reason was already logged server-side by the
// caller, since leaking provider/verification error details to the browser
// isn't useful and could expose internal details.
func redirectSSOError(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login?ssoerror=1", http.StatusSeeOther)
}
