package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestConfigureRegionWritesSelectedRegion(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(tmpDir + "/config.json")
	var out bytes.Buffer
	ok, err := manager.ConfigureRegion("us-west-2", false, strings.NewReader(""), &out)
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
	if config["api_host"] != RegionURLMap["us-west-2"] {
		t.Fatalf("unexpected config: %#v", config)
	}
	if !strings.Contains(out.String(), "Configuration updated") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if !strings.Contains(out.String(), "Connext Cloud is in preview. Do not use in production.") {
		t.Fatalf("missing preview disclaimer: %s", out.String())
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

func TestWriteConfigCreatesRticloudDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	manager := New(filepath.Join(tmpDir, ".rticloud", "config.json"))
	if err := manager.WriteConfig(map[string]string{"api_host": RegionURLMap["us-west-2"]}); err != nil {
		t.Fatal(err)
	}
	config, err := manager.GetConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got := config["api_host"]; got != RegionURLMap["us-west-2"] {
		t.Fatalf("unexpected config value: %q", got)
	}
}

func TestWriteConfigUsesUserOnlyPermissions(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, ".rticloud", "config.json")
	manager := New(configPath)
	if err := manager.WriteConfig(map[string]string{"api_host": RegionURLMap["us-west-2"]}); err != nil {
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
