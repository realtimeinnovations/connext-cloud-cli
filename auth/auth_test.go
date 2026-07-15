// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package auth

import (
	"bytes"
	"encoding/json"
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
	out := &bytes.Buffer{}
	manager.Stdout = out

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
	loginOutput := out.String()
	if !strings.Contains(loginOutput, "If the browser does not open, or you're logging in on a remote machine, run: rticloud login --device") {
		t.Fatalf("login output missing device hint: %s", out.String())
	}
	if strings.Contains(loginOutput, openedURL) {
		t.Fatalf("login output leaked authorization URL: %s", loginOutput)
	}
}

func TestDefaultAuth0DomainUsesDevDomainForDevAPIHosts(t *testing.T) {
	tests := []struct {
		name    string
		apiHost string
		want    string
	}{
		{name: "production", apiHost: "https://cloud.rti.com/api/v1", want: "auth.rti.com"},
		{name: "dev cloud", apiHost: "https://test.cloud.dev-rti.com/api/v1", want: "auth.dev-rti.com"},
		{name: "dev local", apiHost: config.RegionURLMap["dev-local"], want: "auth.dev-rti.com"},
		{name: "missing", want: "auth.rti.com"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configValues := map[string]string{}
			if test.apiHost != "" {
				configValues["api_host"] = test.apiHost
			}
			if got := defaultAuth0Domain(configValues); got != test.want {
				t.Fatalf("defaultAuth0Domain() = %q, want %q", got, test.want)
			}
		})
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

func TestLoginWithDeviceFlowCompletesAndDoesNotLeakDeviceCode(t *testing.T) {
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/device":
			if request.Method != http.MethodPost {
				t.Fatalf("device request method = %s, want POST", request.Method)
			}
			var payload deviceAuthorizationRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.ClientID != "client-id" || payload.Audience != "audience-value" || payload.Scope != "scope-value" {
				t.Fatalf("device request payload = %#v", payload)
			}
			_ = json.NewEncoder(writer).Encode(deviceAuthorizationResponse{
				DeviceCode:              "secret-device-code",
				UserCode:                "abcd-efgh",
				VerificationURI:         "https://login.example.test/device",
				VerificationURIComplete: "https://login.example.test/device?user_code=abcd-efgh",
				ExpiresIn:               30,
				Interval:                1,
			})
		case "/api/v1/auth/device/token":
			pollCount++
			var payload deviceTokenRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.DeviceCode != "secret-device-code" || payload.ClientID != "client-id" {
				t.Fatalf("token request payload = %#v", payload)
			}
			if pollCount == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(writer).Encode(deviceTokenResponse{Error: "authorization_pending"})
				return
			}
			_ = json.NewEncoder(writer).Encode(deviceTokenResponse{AccessToken: "token-value", ExpiresIn: 3600})
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	manager, out, openedURL, sleeps := newDeviceFlowTestManager(t, server.URL+"/api/v1")
	token, err := manager.LoginWithDeviceFlow()
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-value" {
		t.Fatalf("LoginWithDeviceFlow() token = %q, want token-value", token)
	}
	if *openedURL != "https://login.example.test/device?user_code=abcd-efgh" {
		t.Fatalf("opened URL = %q, want verification_uri_complete", *openedURL)
	}
	if got, want := *sleeps, []time.Duration{time.Second, time.Second}; !equalDurations(got, want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
	output := out.String()
	wantOutput := "Attempting to automatically open the SSO authorization page in your default browser.\n" +
		"If the browser does not open or you wish to use a different device to authorize this request, open the following URL:\n" +
		"\n" +
		"  https://login.example.test/device\n" +
		"\n" +
		"Then enter the code:\n" +
		"\n" +
		"  ABCD-EFGH\n"
	if output != wantOutput {
		t.Fatalf("output = %q, want %q", output, wantOutput)
	}
	for _, leaked := range []string{"secret-device-code", "verification_uri_complete"} {
		if strings.Contains(output, leaked) {
			t.Fatalf("output leaked %q: %s", leaked, output)
		}
	}
	savedToken, err := manager.GetAccessTokenFromHomeFile()
	if err != nil {
		t.Fatal(err)
	}
	if savedToken != "token-value" {
		t.Fatalf("saved token = %q, want token-value", savedToken)
	}
}

func TestLoginWithDeviceFlowNormalizesDevLocalVerificationURLs(t *testing.T) {
	manager, out, openedURL, _ := newDeviceFlowTestManager(t, "http://localhost:8090")
	requestURLs := []string{}
	manager.HTTPClient = roundTripClient(func(request *http.Request) (*http.Response, error) {
		requestURLs = append(requestURLs, request.URL.String())
		switch request.URL.String() {
		case "http://localhost:8080/api/v1/auth/device":
			return jsonResponse(deviceAuthorizationResponse{
				DeviceCode:              "secret-device-code",
				UserCode:                "CSQH-VWJB",
				VerificationURI:         "http://localhost:8090/auth/device/CSQH-VWJB",
				VerificationURIComplete: "http://localhost:8090/auth/device/CSQH-VWJB?user_code=CSQH-VWJB",
				ExpiresIn:               30,
				Interval:                1,
			})
		case "http://localhost:8080/api/v1/auth/device/token":
			return jsonResponse(deviceTokenResponse{AccessToken: "token-value", ExpiresIn: 3600})
		default:
			return nil, fmt.Errorf("unexpected request URL: %s", request.URL.String())
		}
	})

	if _, err := manager.LoginWithDeviceFlow(); err != nil {
		t.Fatal(err)
	}
	if got, want := requestURLs, []string{"http://localhost:8080/api/v1/auth/device", "http://localhost:8080/api/v1/auth/device/token"}; !equalStrings(got, want) {
		t.Fatalf("request URLs = %v, want %v", got, want)
	}
	if *openedURL != "http://localhost:8080/api/v1/auth/device/CSQH-VWJB?user_code=CSQH-VWJB" {
		t.Fatalf("opened URL = %q, want normalized verification_uri_complete", *openedURL)
	}
	output := out.String()
	if !strings.Contains(output, "  http://localhost:8080/api/v1/auth/device/CSQH-VWJB\n") {
		t.Fatalf("output did not include normalized verification_uri: %s", output)
	}
	if strings.Contains(output, "localhost:8090/auth/device") {
		t.Fatalf("output included unnormalized dev-local URL: %s", output)
	}
}

func TestLoginWithDeviceFlowSlowDownIncreasesPollingInterval(t *testing.T) {
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/device":
			_ = json.NewEncoder(writer).Encode(deviceAuthorizationResponse{
				DeviceCode:              "secret-device-code",
				UserCode:                "ABCD-EFGH",
				VerificationURI:         "https://login.example.test/device",
				VerificationURIComplete: "https://login.example.test/device?user_code=ABCD-EFGH",
				ExpiresIn:               30,
				Interval:                1,
			})
		case "/api/v1/auth/device/token":
			pollCount++
			switch pollCount {
			case 1:
				writer.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(writer).Encode(deviceTokenResponse{Error: "authorization_pending"})
			case 2:
				writer.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(writer).Encode(deviceTokenResponse{Error: "slow_down"})
			case 3:
				writer.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(writer).Encode(deviceTokenResponse{Error: "authorization_pending"})
			default:
				_ = json.NewEncoder(writer).Encode(deviceTokenResponse{AccessToken: "token-value", ExpiresIn: 3600})
			}
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	manager, _, _, sleeps := newDeviceFlowTestManager(t, server.URL+"/api/v1")
	if _, err := manager.LoginWithDeviceFlow(); err != nil {
		t.Fatal(err)
	}
	if got, want := *sleeps, []time.Duration{time.Second, time.Second, 6 * time.Second, 6 * time.Second}; !equalDurations(got, want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
}

func TestLoginWithDeviceFlowDefaultsInvalidInterval(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/device":
			_ = json.NewEncoder(writer).Encode(deviceAuthorizationResponse{
				DeviceCode:              "secret-device-code",
				UserCode:                "ABCD-EFGH",
				VerificationURI:         "https://login.example.test/device",
				VerificationURIComplete: "https://login.example.test/device?user_code=ABCD-EFGH",
				ExpiresIn:               30,
				Interval:                0,
			})
		case "/api/v1/auth/device/token":
			_ = json.NewEncoder(writer).Encode(deviceTokenResponse{AccessToken: "token-value", ExpiresIn: 3600})
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	manager, _, _, sleeps := newDeviceFlowTestManager(t, server.URL+"/api/v1")
	if _, err := manager.LoginWithDeviceFlow(); err != nil {
		t.Fatal(err)
	}
	if got, want := *sleeps, []time.Duration{5 * time.Second}; !equalDurations(got, want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
}

func TestDeviceAuthBaseURLNormalizesDevLocal8090(t *testing.T) {
	tests := []struct {
		name    string
		apiHost string
		want    string
	}{
		{name: "dev local configured region", apiHost: "http://localhost:8090", want: "http://localhost:8080/api/v1"},
		{name: "web dev proxy keeps api prefix", apiHost: "http://localhost:8080/api/v1", want: "http://localhost:8080/api/v1"},
		{name: "production keeps api prefix", apiHost: "https://cloud.rti.com/api/v1", want: "https://cloud.rti.com/api/v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deviceAuthBaseURL(test.apiHost); got != test.want {
				t.Fatalf("deviceAuthBaseURL(%q) = %q, want %q", test.apiHost, got, test.want)
			}
		})
	}
}

func TestDeviceAuthURLUsesWebAPIBaseForDevLocal(t *testing.T) {
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{name: "verification uri from manager", rawURL: "http://localhost:8090/auth/device/CSQH-VWJB", want: "http://localhost:8080/api/v1/auth/device/CSQH-VWJB"},
		{name: "verification complete from manager", rawURL: "http://localhost:8090/auth/device/CSQH-VWJB?user_code=CSQH-VWJB", want: "http://localhost:8080/api/v1/auth/device/CSQH-VWJB?user_code=CSQH-VWJB"},
		{name: "production unchanged", rawURL: "https://cloud.rti.com/api/v1/auth/device/CSQH-VWJB", want: "https://cloud.rti.com/api/v1/auth/device/CSQH-VWJB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := deviceAuthURL(test.rawURL); got != test.want {
				t.Fatalf("deviceAuthURL(%q) = %q, want %q", test.rawURL, got, test.want)
			}
		})
	}
}

func TestLoginWithDeviceFlowRejectsMissingInitiationFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/device":
			_ = json.NewEncoder(writer).Encode(deviceAuthorizationResponse{
				UserCode:                "ABCD-EFGH",
				VerificationURI:         "https://login.example.test/device",
				VerificationURIComplete: "https://login.example.test/device?user_code=ABCD-EFGH",
				ExpiresIn:               30,
				Interval:                1,
			})
		case "/api/v1/auth/device/token":
			t.Fatal("token polling should not start for invalid initiation response")
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	manager, out, _, _ := newDeviceFlowTestManager(t, server.URL+"/api/v1")
	token, err := manager.LoginWithDeviceFlow()
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("LoginWithDeviceFlow() token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "missing required fields") {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected device instructions for invalid response: %s", out.String())
	}
}

func TestLoginWithDeviceFlowReportsInitiationNotFoundAsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/device":
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte("<!doctype html><title>404 Not Found</title>"))
		case "/api/v1/auth/device/token":
			t.Fatal("token polling should not start when device authorization is unavailable")
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	manager, out, _, _ := newDeviceFlowTestManager(t, server.URL+"/api/v1")
	token, err := manager.LoginWithDeviceFlow()
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("LoginWithDeviceFlow() token = %q, want empty", token)
	}
	if got, want := err.Error(), "Error: Device authorization not available on this Connext Cloud server"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected device instructions for unavailable server: %s", out.String())
	}
}

func TestLoginWithDeviceFlowFormatsNonDeviceTokenErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/device":
			_ = json.NewEncoder(writer).Encode(deviceAuthorizationResponse{
				DeviceCode:              "secret-device-code",
				UserCode:                "ABCD-EFGH",
				VerificationURI:         "https://login.example.test/device",
				VerificationURIComplete: "https://login.example.test/device?user_code=ABCD-EFGH",
				ExpiresIn:               30,
				Interval:                1,
			})
		case "/api/v1/auth/device/token":
			writer.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(writer).Encode(map[string]string{"message": "device flow is disabled"})
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	manager, _, _, _ := newDeviceFlowTestManager(t, server.URL+"/api/v1")
	token, err := manager.LoginWithDeviceFlow()
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("LoginWithDeviceFlow() token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "device flow is disabled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoginWithDeviceFlowTerminalErrors(t *testing.T) {
	tests := []struct {
		name      string
		code      string
		wantError string
	}{
		{name: "access denied", code: "access_denied", wantError: "denied"},
		{name: "expired token", code: "expired_token", wantError: "expired"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v1/auth/device":
					_ = json.NewEncoder(writer).Encode(deviceAuthorizationResponse{
						DeviceCode:              "secret-device-code",
						UserCode:                "ABCD-EFGH",
						VerificationURI:         "https://login.example.test/device",
						VerificationURIComplete: "https://login.example.test/device?user_code=ABCD-EFGH",
						ExpiresIn:               30,
						Interval:                1,
					})
				case "/api/v1/auth/device/token":
					writer.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(writer).Encode(deviceTokenResponse{Error: test.code})
				default:
					t.Fatalf("unexpected path: %s", request.URL.Path)
				}
			}))
			defer server.Close()

			manager, _, _, _ := newDeviceFlowTestManager(t, server.URL+"/api/v1")
			token, err := manager.LoginWithDeviceFlow()
			if err == nil {
				t.Fatal("expected error")
			}
			if token != "" {
				t.Fatalf("LoginWithDeviceFlow() token = %q, want empty", token)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if _, err := os.Stat(manager.TokenPath); !os.IsNotExist(err) {
				t.Fatalf("token file stat error = %v, want not exist", err)
			}
		})
	}
}

func TestLoginWithDeviceFlowExpiresLocally(t *testing.T) {
	pollCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/device":
			_ = json.NewEncoder(writer).Encode(deviceAuthorizationResponse{
				DeviceCode:              "secret-device-code",
				UserCode:                "ABCD-EFGH",
				VerificationURI:         "https://login.example.test/device",
				VerificationURIComplete: "https://login.example.test/device?user_code=ABCD-EFGH",
				ExpiresIn:               2,
				Interval:                1,
			})
		case "/api/v1/auth/device/token":
			pollCount++
			writer.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(writer).Encode(deviceTokenResponse{Error: "authorization_pending"})
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	manager, _, _, _ := newDeviceFlowTestManager(t, server.URL+"/api/v1")
	token, err := manager.LoginWithDeviceFlow()
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("LoginWithDeviceFlow() token = %q, want empty", token)
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Fatalf("unexpected error: %v", err)
	}
	if pollCount != 1 {
		t.Fatalf("poll count = %d, want 1", pollCount)
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

func jsonResponse(payload any) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}, nil
}

func newDeviceFlowTestManager(t *testing.T, apiHost string) (*Manager, *bytes.Buffer, *string, *[]time.Duration) {
	t.Helper()
	manager := New(staticConfigProvider{values: map[string]string{
		"api_host": apiHost,
		"audience": "audience-value",
		"scope":    "scope-value",
	}}, filepath.Join(t.TempDir(), "credentials.json"))
	out := &bytes.Buffer{}
	openedURL := ""
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	sleeps := []time.Duration{}
	manager.Stdout = out
	manager.Now = func() time.Time { return now }
	manager.Sleep = func(duration time.Duration) {
		sleeps = append(sleeps, duration)
		now = now.Add(duration)
	}
	manager.OpenBrowser = func(target string) error {
		openedURL = target
		return errors.New("browser unavailable")
	}
	return manager, out, &openedURL, &sleeps
}

func equalDurations(left []time.Duration, right []time.Duration) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
