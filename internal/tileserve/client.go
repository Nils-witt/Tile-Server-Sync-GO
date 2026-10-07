// Package tileserve is a minimal client for the tileserve-go HTTP API
// (see https://github.com/Nils-witt/Tileserve-GO), covering just what's
// needed to authenticate, list maps, and fetch a map version's geo objects.
package tileserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a small, synchronous tileserve-go API client.
type Client struct {
	baseURL    string
	httpClient *http.Client
	token      string

	// username and password are retained from Login so a request that gets
	// a 401 (e.g. an expired token) can transparently re-login and retry
	// once. Left unset when the token was supplied directly via SetToken,
	// in which case a 401 is simply returned to the caller.
	username string
	password string
}

// New creates a Client for the given base URL (e.g. "http://localhost:8085").
func New(baseURL string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetToken sets the bearer token used for subsequent requests, bypassing
// Login. Useful when a token was obtained out of band.
func (c *Client) SetToken(token string) {
	c.token = token
}

// SetCredentials records a username/password for the client to log in with
// lazily, on its first request (and again whenever a request gets a 401),
// instead of logging in up front with Login.
func (c *Client) SetCredentials(username, password string) {
	c.username = username
	c.password = password
}

// Ping checks that the base URL answers HTTP at all (any status code
// counts), for testing connection settings when no login is involved, i.e.
// when a token was configured directly.
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return fmt.Errorf("build ping request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ping request: %w", err)
	}

	return resp.Body.Close()
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

// Login exchanges a username/password for a JWT (POST /login) and stores it
// for use by subsequent requests on this client.
func (c *Client) Login(ctx context.Context, username, password string) error {
	body, err := json.Marshal(loginRequest{Username: username, Password: password}) //nolint:gosec // password is a request field, not a hardcoded secret
	if err != nil {
		return fmt.Errorf("encode login request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build login request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read login response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed (%s): %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var lr loginResponse
	if err := json.Unmarshal(respBody, &lr); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}

	if lr.Token == "" {
		return errors.New("login response did not include a token")
	}

	c.token = lr.Token
	c.username = username
	c.password = password

	return nil
}

// GeoObject mirrors the GeoObject schema from openapi.yaml.
type GeoObject struct {
	UUID         string    `json:"uuid"`
	MapUUID      string    `json:"mapUuid"`
	Version      string    `json:"version"`
	Name         string    `json:"name"`
	ExternalID   string    `json:"externalId"`
	Latitude     float64   `json:"latitude"`
	Longitude    float64   `json:"longitude"`
	Street       string    `json:"street"`
	Housenumber  string    `json:"housenumber"`
	Postcode     string    `json:"postcode"`
	City         string    `json:"city"`
	CityDistrict string    `json:"cityDistrict"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
	CreatedBy    string    `json:"createdBy"`
	UpdatedBy    string    `json:"updatedBy"`
}

// GeoObjects fetches every geo object for a given map id and version via
// GET /maps/{id}/version/{version}/geo-objects. version may be a real
// numeric version, the literal "current", or a user-defined alias.
func (c *Client) GeoObjects(ctx context.Context, mapID, version string) ([]GeoObject, error) {
	path := fmt.Sprintf("/maps/%s/version/%s/geo-objects", url.PathEscape(mapID), url.PathEscape(version))

	var objects []GeoObject
	if err := c.getJSON(ctx, path, fmt.Sprintf("geo-objects request for map %s version %s", mapID, version),
		&objects); err != nil {
		return nil, err
	}

	return objects, nil
}

// RemoteMap is the subset of openapi.yaml's Map schema this tool uses.
type RemoteMap struct {
	UUID           string `json:"uuid"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	CurrentVersion string `json:"currentVersion"`
}

// Maps lists every map the authenticated user may see via GET /maps.
func (c *Client) Maps(ctx context.Context) ([]RemoteMap, error) {
	var maps []RemoteMap
	if err := c.getJSON(ctx, "/maps", "maps request", &maps); err != nil {
		return nil, err
	}

	return maps, nil
}

// getJSON performs an authenticated GET of path and JSON-decodes a 200
// response into out. A 401 triggers one transparent re-login and retry when
// the client logged in with a username/password. desc names the request in
// error messages.
func (c *Client) getJSON(ctx context.Context, path, desc string, out any) error {
	if c.token == "" {
		if c.username == "" {
			return errors.New("client is not authenticated: call Login, SetCredentials or SetToken first")
		}

		if err := c.Login(ctx, c.username, c.password); err != nil {
			return fmt.Errorf("login for %s: %w", desc, err)
		}
	}

	body, status, err := c.getOnce(ctx, path, desc)
	if err != nil {
		return err
	}

	if status == http.StatusUnauthorized && c.username != "" {
		log.Printf("%s got 401, re-authenticating", desc)

		if err := c.Login(ctx, c.username, c.password); err != nil {
			return fmt.Errorf("re-login after 401 for %s: %w", desc, err)
		}

		body, status, err = c.getOnce(ctx, path, desc)
		if err != nil {
			return err
		}
	}

	if status != http.StatusOK {
		return fmt.Errorf("%s failed (%d): %s", desc, status, strings.TrimSpace(string(body)))
	}

	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s response: %w", desc, err)
	}

	return nil
}

// getOnce performs a single authenticated GET of path and returns the raw
// response body and status code without interpreting non-200 statuses, so
// the caller can decide whether to retry (e.g. after a 401) before treating
// the status as an error.
func (c *Client) getOnce(ctx context.Context, path, desc string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build %s: %w", desc, err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", desc, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s response: %w", desc, err)
	}

	return body, resp.StatusCode, nil
}
