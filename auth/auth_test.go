// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package auth

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/config"
)

type stubConfigProvider struct{}

func (stubConfigProvider) GetConfig() (map[string]string, error) { return map[string]string{}, nil }
func (stubConfigProvider) GetClientID() string                   { return "client-id" }
func (stubConfigProvider) RequireConfiguration(io.Writer) bool   { return true }

type staticConfigProvider struct {
	values map[string]string
}

func (provider staticConfigProvider) GetConfig() (map[string]string, error) {
	return provider.values, nil
}

func (staticConfigProvider) GetClientID() string                 { return "client-id" }
func (staticConfigProvider) RequireConfiguration(io.Writer) bool { return true }

func TestNewUsesRticloudCredentialsPath(t *testing.T) {
	manager := New(stubConfigProvider{}, "")
	if got, want := manager.TokenPath, config.DefaultCredentialsPath(); got != want {
		t.Fatalf("TokenPath = %q, want %q", got, want)
	}
}

func TestDefaultWorkspacesCredentialsPath(t *testing.T) {
	if got := DefaultWorkspacesCredentialsPath(); !strings.HasSuffix(got, filepath.Join(".rticloud", "workspaces_credentials.json")) {
		t.Fatalf("DefaultWorkspacesCredentialsPath() = %q", got)
	}
}

func TestSaveAccessTokenCreatesRticloudDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, ".rticloud", "credentials.json")
	manager := New(stubConfigProvider{}, tokenPath)
	manager.Now = func() time.Time { return time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC) }
	if err := manager.SaveAccessToken("token-value", 3600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatal(err)
	}
	token, err := manager.GetAccessTokenFromHomeFile()
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-value" {
		t.Fatalf("GetAccessTokenFromHomeFile() = %q, want token-value", token)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credentials mode = %#o, want 0o600", mode)
	}
}

func TestGetAccessTokenFromHomeFileReturnsReadError(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(stubConfigProvider{}, tmpDir)

	token, err := manager.GetAccessTokenFromHomeFile()
	if err == nil {
		t.Fatal("expected read error")
	}
	if token != "" {
		t.Fatalf("GetAccessTokenFromHomeFile() token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected directory read error, got %v", err)
	}
}

func TestGetAccessTokenForCLIReturnsHomeFileErrorBeforeAPIKeyAuth(t *testing.T) {
	tmpDir := t.TempDir()
	apiKeyErr := errors.New("api key auth should not run")
	manager := New(stubAPIConfigProvider{apiURL: "https://example.test"}, tmpDir)
	manager.Env = func(name string) string {
		if name == "CONNEXT_CLOUD_API_KEY" {
			return "invalid-key"
		}
		return ""
	}
	manager.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		return nil, apiKeyErr
	})

	token, err := manager.GetAccessTokenForCLI()
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("GetAccessTokenForCLI() token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected directory read error, got %v", err)
	}
	if errors.Is(err, apiKeyErr) {
		t.Fatalf("expected home file error before API key auth, got %v", err)
	}
}

func TestGetAccessTokenForCLIReturnsAPIKeyExchangeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte("bad api key"))
	}))
	defer server.Close()

	manager := New(stubConfigProvider{}, filepath.Join(t.TempDir(), "credentials.json"))
	manager.Env = func(name string) string {
		if name == "CONNEXT_CLOUD_API_KEY" {
			return "invalid-key"
		}
		return ""
	}
	manager.Config = stubAPIConfigProvider{apiURL: server.URL}

	token, err := manager.GetAccessTokenForCLI()
	if err == nil {
		t.Fatal("expected API key exchange error")
	}
	if token != "" {
		t.Fatalf("GetAccessTokenForCLI() token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "Error authenticating with API key: 401 - bad api key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetAuthHeadersPreservesAPIKeyExchangeError(t *testing.T) {
	expectedErr := errors.New("api key exchange failed")
	manager := New(stubConfigProvider{}, filepath.Join(t.TempDir(), "credentials.json"))
	manager.Env = func(name string) string {
		if name == "CONNEXT_CLOUD_API_KEY" {
			return "invalid-key"
		}
		return ""
	}
	manager.Config = stubAPIConfigProvider{apiURL: "https://example.test"}
	manager.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		return nil, expectedErr
	})

	headers, err := manager.GetAuthHeaders()
	if err == nil {
		t.Fatal("expected error")
	}
	if headers != nil {
		t.Fatalf("GetAuthHeaders() headers = %#v, want nil", headers)
	}
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected %v, got %v", expectedErr, err)
	}
}

func TestLoginIncludesStateAndExchangesCode(t *testing.T) {
	manager := New(staticConfigProvider{values: map[string]string{
		"api_host":     "https://example.test/api/v1",
		"auth0_domain": "auth.example.test",
		"audience":     "https://cloud.example.test/api/v1",
		"scope":        "openid profile email",
	}}, filepath.Join(t.TempDir(), "credentials.json"))

	var openedURL string
	manager.OpenBrowser = func(target string) error {
		openedURL = target
		parsedURL, err := url.Parse(target)
		if err != nil {
			return err
		}
		state := parsedURL.Query().Get("state")
		if state == "" {
			return fmt.Errorf("missing state parameter")
		}
		callbackURL := parsedURL.Query().Get("redirect_uri")
		_, err = http.Get(callbackURL + "?code=auth-code&state=" + url.QueryEscape(state))
		return err
	}
	manager.HTTPClient = roundTripClient(func(request *http.Request) (*http.Response, error) {
		if got, want := request.URL.String(), "https://auth.example.test/oauth/token"; got != want {
			return nil, fmt.Errorf("token request URL = %q, want %q", got, want)
		}
		if err := request.ParseForm(); err != nil {
			return nil, err
		}
		if got := request.Form.Get("code"); got != "auth-code" {
			return nil, fmt.Errorf("code = %q, want auth-code", got)
		}
		if got := request.Form.Get("code_verifier"); got == "" {
			return nil, fmt.Errorf("missing code_verifier")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"access_token":"token-value","expires_in":3600}`)),
		}, nil
	})

	token, err := manager.Login()
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-value" {
		t.Fatalf("Login() token = %q, want token-value", token)
	}
	if openedURL == "" {
		t.Fatal("expected browser URL to be opened")
	}
	parsedURL, err := url.Parse(openedURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsedURL.Query().Get("state"); got == "" {
		t.Fatal("expected state parameter in authorization URL")
	}
	gotRedirectURI := parsedURL.Query().Get("redirect_uri")
	if !strings.HasPrefix(gotRedirectURI, "http://localhost:") || !strings.HasSuffix(gotRedirectURI, "/callback") {
		t.Fatalf("redirect_uri = %q, want http://localhost:{port}/callback", gotRedirectURI)
	}
}

func TestLoginRejectsMismatchedState(t *testing.T) {
	manager := New(staticConfigProvider{values: map[string]string{
		"api_host": "https://example.test/api/v1",
	}}, filepath.Join(t.TempDir(), "credentials.json"))
	manager.OpenBrowser = func(target string) error {
		parsedURL, err := url.Parse(target)
		if err != nil {
			return err
		}
		callbackURL := parsedURL.Query().Get("redirect_uri")
		_, err = http.Get(callbackURL + "?code=auth-code&state=wrong-state")
		return err
	}
	manager.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("token exchange should not run for invalid state")
	})

	token, err := manager.Login()
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("Login() token = %q, want empty", token)
	}
	if err.Error() != "Error: Invalid OAuth state parameter." {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginReturnsOAuthCallbackError(t *testing.T) {
	manager := New(staticConfigProvider{values: map[string]string{
		"api_host": "https://example.test/api/v1",
	}}, filepath.Join(t.TempDir(), "credentials.json"))
	manager.OpenBrowser = func(target string) error {
		parsedURL, err := url.Parse(target)
		if err != nil {
			return err
		}
		state := parsedURL.Query().Get("state")
		callbackURL := parsedURL.Query().Get("redirect_uri")
		query := url.Values{
			"error": {"<script>alert(document.domain)</script>"},
			"state": {state},
		}
		response, err := http.Get(callbackURL + "?" + query.Encode())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if got, want := response.Header.Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
			return fmt.Errorf("callback Content-Type = %q, want %q", got, want)
		}
		if got, want := response.Header.Get("X-Content-Type-Options"), "nosniff"; got != want {
			return fmt.Errorf("callback X-Content-Type-Options = %q, want %q", got, want)
		}
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "<script>") {
			return fmt.Errorf("callback reflected OAuth error in response body: %q", body)
		}
		return nil
	}
	manager.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("token exchange should not run for callback error")
	})

	token, err := manager.Login()
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("Login() token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "<script>alert(document.domain)</script>") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginReturnsOAuthTokenExchangeDetails(t *testing.T) {
	manager := New(staticConfigProvider{values: map[string]string{
		"api_host":     "https://example.test/api/v1",
		"auth0_domain": "auth.example.test",
	}}, filepath.Join(t.TempDir(), "credentials.json"))
	manager.OpenBrowser = func(target string) error {
		parsedURL, err := url.Parse(target)
		if err != nil {
			return err
		}
		state := parsedURL.Query().Get("state")
		callbackURL := parsedURL.Query().Get("redirect_uri")
		_, err = http.Get(callbackURL + "?code=auth-code&state=" + url.QueryEscape(state))
		return err
	}
	manager.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant","error_description":"bad verifier"}`)),
		}, nil
	})

	token, err := manager.Login()
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("Login() token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "invalid_grant: bad verifier") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type stubAPIConfigProvider struct {
	apiURL string
}

func (provider stubAPIConfigProvider) GetConfig() (map[string]string, error) {
	return map[string]string{"api_host": provider.apiURL}, nil
}

func (stubAPIConfigProvider) GetClientID() string                 { return "client-id" }
func (stubAPIConfigProvider) RequireConfiguration(io.Writer) bool { return true }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func roundTripClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}
