// Package tileserve is a minimal client for the tileserve-go HTTP API
// (see https://github.com/Nils-witt/Tileserve-GO), covering just what's
// needed to authenticate and fetch a map version's geo objects.
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
	if c.token == "" {
		return nil, errors.New("client is not authenticated: call Login or SetToken first")
	}

	body, status, err := c.geoObjectsOnce(ctx, mapID, version)
	if err != nil {
		return nil, err
	}

	if status == http.StatusUnauthorized && c.username != "" {
		log.Printf("geo-objects request for map %s version %s got 401, re-authenticating", mapID, version)

		if err := c.Login(ctx, c.username, c.password); err != nil {
			return nil, fmt.Errorf("re-login after 401 for map %s version %s: %w", mapID, version, err)
		}

		body, status, err = c.geoObjectsOnce(ctx, mapID, version)
		if err != nil {
			return nil, err
		}
	}

	if status != http.StatusOK {
		return nil, fmt.Errorf("geo-objects request for map %s version %s failed (%d): %s",
			mapID, version, status, strings.TrimSpace(string(body)))
	}

	var objects []GeoObject
	if err := json.Unmarshal(body, &objects); err != nil {
		return nil, fmt.Errorf("decode geo-objects response: %w", err)
	}

	return objects, nil
}

// geoObjectsOnce performs a single GET /maps/{id}/version/{version}/geo-objects
// request and returns the raw response body and status code without
// interpreting non-200 statuses, so the caller can decide whether to retry
// (e.g. after a 401) before treating the status as an error.
func (c *Client) geoObjectsOnce(ctx context.Context, mapID, version string) ([]byte, int, error) {
	reqURL := fmt.Sprintf("%s/maps/%s/version/%s/geo-objects",
		c.baseURL, url.PathEscape(mapID), url.PathEscape(version))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build geo-objects request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("geo-objects request for map %s version %s: %w", mapID, version, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read geo-objects response: %w", err)
	}

	return body, resp.StatusCode, nil
}
