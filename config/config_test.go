package config

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

func TestDefaultPathsUseRticloudDirectory(t *testing.T) {
	path := DefaultConfigPath()
	if got, want := filepath.Base(path), "config.json"; got != want {
		t.Fatalf("DefaultConfigPath() base = %q, want %q", got, want)
	}
	if got, want := filepath.Base(filepath.Dir(path)), ".rticloud"; got != want {
		t.Fatalf("DefaultConfigPath() dir = %q, want %q", got, want)
	}
	credentialsPath := DefaultCredentialsPath()
	if got, want := filepath.Base(credentialsPath), "credentials.json"; got != want {
		t.Fatalf("DefaultCredentialsPath() base = %q, want %q", got, want)
	}
	if got, want := filepath.Base(filepath.Dir(credentialsPath)), ".rticloud"; got != want {
		t.Fatalf("DefaultCredentialsPath() dir = %q, want %q", got, want)
	}
}

func newAuthConfigTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/auth/config" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"auth0_domain":"auth.test","client_id":"test-client","audience":"https://audience.test/api/v1","scope":"ignored:scope"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func useTestRegionURL(t *testing.T, region string, apiHost string) {
	t.Helper()
	previousValue, existed := RegionURLMap[region]
	RegionURLMap[region] = apiHost
	t.Cleanup(func() {
		if existed {
			RegionURLMap[region] = previousValue
			return
		}
		delete(RegionURLMap, region)
	})
}

func TestConfigureRegionWritesSelectedRegion(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	server := newAuthConfigTestServer(t)
	manager.HTTPClient = server.Client()
	useTestRegionURL(t, "us-east-2", server.URL+"/api/v1")
	var out bytes.Buffer
	ok, err := manager.ConfigureRegion("us-east-2", false, strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected success")
	}
	config, err := manager.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config["api_host"] != RegionURLMap["us-east-2"] {
		t.Fatalf("unexpected config: %#v", config)
	}
	checks := map[string]string{
		"auth0_domain":    "auth.test",
		"auth0_client_id": "test-client",
		"audience":        "https://audience.test/api/v1",
	}
	for key, want := range checks {
		if got := config[key]; got != want {
			t.Fatalf("config[%q] = %q, want %q", key, got, want)
		}
	}
	if got := config["scope"]; got != "" {
		t.Fatalf("scope = %q, want empty because configure ignores auth config scopes", got)
	}
	if !strings.Contains(out.String(), "Configuration updated") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if !strings.Contains(out.String(), previewWarning) {
		t.Fatalf("missing preview disclaimer: %s", out.String())
	}
}

func TestConfigureRegionFallsBackToDefaultsWhenAuthConfigUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	if err := manager.WriteConfig(map[string]string{
		"api_host":        "https://old.example/api/v1",
		"auth0_domain":    "old-auth.example",
		"auth0_client_id": "old-client",
		"audience":        "https://old.example/api/v1",
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "<!doctype html><title>404 Not Found</title>", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	manager.HTTPClient = server.Client()
	useTestRegionURL(t, "us-east-2", server.URL+"/api/v1")
	var out bytes.Buffer
	ok, err := manager.ConfigureRegion("us-east-2", false, strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected success")
	}
	config, err := manager.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := config["api_host"], server.URL+"/api/v1"; got != want {
		t.Fatalf("api_host = %q, want %q", got, want)
	}
	for _, key := range []string{"auth0_domain", "auth0_client_id", "audience"} {
		if got := config[key]; got != "" {
			t.Fatalf("config[%q] = %q, want empty fallback default", key, got)
		}
	}
	rendered := out.String()
	if !strings.Contains(rendered, "Warning: Could not load auth configuration; using built-in defaults.") {
		t.Fatalf("missing warning: %s", rendered)
	}
	if strings.Contains(rendered, "<!doctype") || strings.Contains(rendered, "GET /auth/config failed") {
		t.Fatalf("warning leaked raw auth config error: %s", rendered)
	}
	if !strings.Contains(rendered, "Configuration updated") {
		t.Fatalf("missing success message: %s", rendered)
	}
}

func TestConfigureRegionPromptsWithSharedSelector(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	server := newAuthConfigTestServer(t)
	manager.HTTPClient = server.Client()
	useTestRegionURL(t, "us-east-2", server.URL+"/api/v1")
	var out bytes.Buffer
	ok, err := manager.ConfigureRegion("", false, strings.NewReader("1\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected success")
	}
	rendered := tui.StripANSIEscapes(out.String())
	config, err := manager.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config["api_host"] != RegionURLMap["us-east-2"] {
		t.Fatalf("unexpected config: %#v", config)
	}
	checks := []string{
		"┌─ RTI Connext Cloud",
		"Welcome!",
		previewWarning,
		"rticloud dev",
		"Select region:",
		"1. us-east-2",
		"cloud.rti.com",
		"2. eu-central-1",
		"eu-central-1.cloud.rti.com",
		"3. Custom domain",
	}
	for _, check := range checks {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing %q in output: %s", check, rendered)
		}
	}
	if strings.Index(rendered, "Select region:") < strings.Index(rendered, "rticloud dev") {
		t.Fatalf("expected region prompt after welcome box: %s", rendered)
	}
}

func TestConfigureRegionPromptsForCustomCloudDomain(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	server := newAuthConfigTestServer(t)
	manager.HTTPClient = server.Client()
	var out bytes.Buffer
	ok, err := manager.ConfigureRegion("", false, strings.NewReader("3\n"+strings.TrimPrefix(server.URL, "https://")+"\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected success")
	}
	config, err := manager.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := config["api_host"], server.URL+"/api/v1"; got != want {
		t.Fatalf("api_host = %q, want %q", got, want)
	}
	rendered := tui.StripANSIEscapes(out.String())
	for _, check := range []string{"3. Custom domain", "Enter the full domain by hand", "Enter full cloud domain", "Configuration updated"} {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing %q in output: %s", check, rendered)
		}
	}
}

func TestConfigureRegionCustomCloudDomainAcceptsURL(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	server := newAuthConfigTestServer(t)
	manager.HTTPClient = server.Client()
	var out bytes.Buffer
	ok, err := manager.ConfigureRegion("", false, strings.NewReader("3\n"+server.URL+"/api/v1\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected success")
	}
	config, err := manager.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := config["api_host"], server.URL+"/api/v1"; got != want {
		t.Fatalf("api_host = %q, want %q", got, want)
	}
}

func TestCustomDomainAPIHostRejectsURLPartsOutsideHostAndPath(t *testing.T) {
	for _, value := range []string{
		"example.com?x=y",
		"example.com#section",
		"user@example.com",
		"https://user@example.com",
		"https://example.com?x=y",
		"https://example.com#section",
	} {
		t.Run(value, func(t *testing.T) {
			if got, err := customDomainAPIHost(value); err == nil {
				t.Fatalf("customDomainAPIHost(%q) = %q, want error", value, got)
			}
		})
	}
}

func TestConfigureRegionUsesDefaultForBlankPromptSelection(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	server := newAuthConfigTestServer(t)
	manager.HTTPClient = server.Client()
	useTestRegionURL(t, "us-east-2", server.URL+"/api/v1")
	var out bytes.Buffer
	ok, err := manager.ConfigureRegion("", false, strings.NewReader("\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected success")
	}
	config, err := manager.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config["api_host"] != RegionURLMap["us-east-2"] {
		t.Fatalf("unexpected config: %#v", config)
	}
}

func TestGetRegionReportsCustomHost(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	if err := manager.WriteConfig(map[string]string{"api_host": "https://custom.example"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err := manager.ConfigureRegion("", true, strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Current region: custom") || !strings.Contains(out.String(), "Current API host: https://custom.example") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestGetAPIURLReturnsNotConfiguredError(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(filepath.Join(tmpDir, "config.json"))
	_, err := manager.GetAPIURL()
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("GetAPIURL() error = %v, want ErrNotConfigured", err)
	}
}

func TestGetRegionReportsNotConfiguredWhenNoAPIHost(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	var out bytes.Buffer
	_, err := manager.ConfigureRegion("", true, strings.NewReader(""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "Current region: not configured" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestGetClientIDUsesEnvironmentFirst(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	if err := manager.WriteConfig(map[string]string{"auth0_client_id": "config-client"}); err != nil {
		t.Fatal(err)
	}
	previousDefault := defaultClientID
	defaultClientID = "build-client"
	t.Cleanup(func() { defaultClientID = previousDefault })
	manager.Env = func(key string) string {
		if key == "CONNEXT_CLOUD_CLI_CLIENT_ID" {
			return "env-client"
		}
		return ""
	}
	if got := manager.GetClientID(); got != "env-client" {
		t.Fatalf("GetClientID() = %q, want env-client", got)
	}
}

func TestGetClientIDUsesConfigBeforeBuildTimeDefault(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	if err := manager.WriteConfig(map[string]string{"auth0_client_id": "config-client"}); err != nil {
		t.Fatal(err)
	}
	previousDefault := defaultClientID
	defaultClientID = "build-client"
	t.Cleanup(func() { defaultClientID = previousDefault })
	manager.Env = func(string) string { return "" }
	if got := manager.GetClientID(); got != "config-client" {
		t.Fatalf("GetClientID() = %q, want config-client", got)
	}
}

func TestGetClientIDFallsBackToBuildTimeDefault(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	previousDefault := defaultClientID
	defaultClientID = "build-client"
	t.Cleanup(func() { defaultClientID = previousDefault })
	manager.Env = func(string) string { return "" }
	if got := manager.GetClientID(); got != "build-client" {
		t.Fatalf("GetClientID() = %q, want build-client", got)
	}
}

func TestGetWorkspacesClientIDUsesEnvironmentFirst(t *testing.T) {
	previousDefault := defaultWorkspacesClientID
	defaultWorkspacesClientID = "build-workspaces-client"
	t.Cleanup(func() { defaultWorkspacesClientID = previousDefault })
	got := GetWorkspacesClientID(func(key string) string {
		if key == "CONNEXT_WORKSPACES_CLI_CLIENT_ID" {
			return "env-workspaces-client"
		}
		return ""
	})
	if got != "env-workspaces-client" {
		t.Fatalf("GetWorkspacesClientID() = %q, want env-workspaces-client", got)
	}
}

func TestGetWorkspacesClientIDFallsBackToBuildTimeDefault(t *testing.T) {
	previousDefault := defaultWorkspacesClientID
	defaultWorkspacesClientID = "build-workspaces-client"
	t.Cleanup(func() { defaultWorkspacesClientID = previousDefault })
	if got := GetWorkspacesClientID(func(string) string { return "" }); got != "build-workspaces-client" {
		t.Fatalf("GetWorkspacesClientID() = %q, want build-workspaces-client", got)
	}
}

func TestWriteConfigCreatesRticloudDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(filepath.Join(tmpDir, ".rticloud", "config.json"))
	if err := manager.WriteConfig(map[string]string{"api_host": RegionURLMap["us-east-2"]}); err != nil {
		t.Fatal(err)
	}
	config, err := manager.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := config["api_host"]; got != RegionURLMap["us-east-2"] {
		t.Fatalf("unexpected config value: %q", got)
	}
}

func TestWriteConfigUsesUserOnlyPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".rticloud", "config.json")
	manager := New(configPath)
	if err := manager.WriteConfig(map[string]string{"api_host": RegionURLMap["us-east-2"]}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want %#o", got, 0o600)
	}
}
