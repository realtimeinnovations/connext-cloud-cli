// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/update"
)

func TestParserShowsGeneratedHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute(nil, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for no args (root help), got %v", err)
	}
	if !strings.Contains(out.String(), "Connect to Connext Cloud:") ||
		!strings.Contains(out.String(), "Manage Connext Cloud:") ||
		!strings.Contains(out.String(), "Setup:") ||
		!strings.Contains(out.String(), "rticloud [command] [flags]") ||
		!strings.Contains(out.String(), "gateway") ||
		!strings.Contains(out.String(), "databus") ||
		!strings.Contains(out.String(), "update") ||
		!strings.Contains(out.String(), "--version") {
		t.Fatalf("unexpected root help: %s", out.String())
	}
	if strings.Contains(out.String(), "--disable-ssl-verify") {
		t.Fatalf("deprecated disable SSL flag should not be exposed: %s", out.String())
	}
	if strings.Contains(out.String(), "\n  version") {
		t.Fatalf("version should only be exposed as --version: %s", out.String())
	}

	out.Reset()
	err = Execute([]string{"databus"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for databus (help), got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Databuses") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected databus help: %s", out.String())
	}

	out.Reset()
	err = Execute([]string{"databus", "create", "--help"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for databus create --help, got %v", err)
	}
	if !strings.Contains(out.String(), "--replicas") || !strings.Contains(out.String(), "--observability-service") || !strings.Contains(out.String(), "--non-secure") {
		t.Fatalf("unexpected databus create help: %s", out.String())
	}

	out.Reset()
	err = Execute([]string{"observability", "create", "--help"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for observability create --help, got %v", err)
	}
	if !strings.Contains(out.String(), "--network-name") || !strings.Contains(out.String(), "--non-secure") {
		t.Fatalf("unexpected observability create help: %s", out.String())
	}
}

func TestParserUpdateHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"update", "--help"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for update --help, got %v", err)
	}
	for _, want := range []string{"Update rticloud", "--check", "--force"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in output: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "--yes") {
		t.Fatalf("update help should not expose --yes: %s", out.String())
	}
}

func TestParserUpdateCheck(t *testing.T) {
	var out bytes.Buffer
	runtime := &app.Runtime{Updater: update.New(config.New(filepath.Join(t.TempDir(), "config.json")), &out)}
	runtime.Updater.CurrentVersion = func() string { return "1.2.3" }
	runtime.Updater.Now = func() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }
	runtime.Updater.HTTPClient = roundTripClient(func(request *http.Request) (*http.Response, error) {
		return stringResponse(http.StatusOK, `{"tag_name":"v1.2.4","html_url":"https://example.test/release"}`), nil
	})
	err := Execute([]string{"update", "--check"}, &out, io.Discard, runtime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"Current version: 1.2.3", "Latest version:  1.2.4", "Update available"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in output: %s", want, out.String())
		}
	}
}

func TestParserLoginDeviceFlag(t *testing.T) {
	var out bytes.Buffer
	if err := Execute([]string{"login", "--help"}, &out, io.Discard, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "--device") {
		t.Fatalf("login help missing --device: %s", out.String())
	}
	if strings.Contains(out.String(), "--device-flow") {
		t.Fatalf("login help still includes --device-flow: %s", out.String())
	}
}

func TestParserLoginDeviceRoutesToDeviceLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/auth/device":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"device_code":               "secret-device-code",
				"user_code":                 "ABCD-EFGH",
				"verification_uri":          "https://login.example.test/device",
				"verification_uri_complete": "https://login.example.test/device?user_code=ABCD-EFGH",
				"expires_in":                30,
				"interval":                  1,
			})
		case "/api/v1/auth/device/token":
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "token-value", "expires_in": 3600})
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	defer server.Close()

	var out bytes.Buffer
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	authManager := auth.New(parserAuthConfigProvider{apiHost: server.URL + "/api/v1"}, filepath.Join(t.TempDir(), "credentials.json"))
	authManager.Stdout = &out
	authManager.Now = func() time.Time { return now }
	authManager.Sleep = func(duration time.Duration) { now = now.Add(duration) }
	authManager.OpenBrowser = func(string) error { return nil }
	runtime := &app.Runtime{Auth: authManager}

	if err := Execute([]string{"login", "--device"}, &out, io.Discard, runtime); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	token, err := authManager.GetAccessTokenFromHomeFile()
	if err != nil {
		t.Fatal(err)
	}
	if token != "token-value" {
		t.Fatalf("saved token = %q, want token-value", token)
	}
	if !strings.Contains(out.String(), "ABCD-EFGH") {
		t.Fatalf("device instructions missing user code: %s", out.String())
	}
}

func TestParserPrintAccessToken(t *testing.T) {
	var out bytes.Buffer
	authManager := auth.New(parserAuthConfigProvider{}, filepath.Join(t.TempDir(), "credentials.json"))
	if err := authManager.SaveAccessToken("token-value", 3600); err != nil {
		t.Fatal(err)
	}

	if err := Execute([]string{"print-access-token"}, &out, io.Discard, &app.Runtime{Auth: authManager}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := out.String(), "token-value\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestParserPrintAccessTokenRequiresExistingLogin(t *testing.T) {
	var out bytes.Buffer
	authManager := auth.New(parserAuthConfigProvider{}, filepath.Join(t.TempDir(), "credentials.json"))

	err := Execute([]string{"print-access-token"}, &out, io.Discard, &app.Runtime{Auth: authManager})
	if err == nil {
		t.Fatal("expected authentication error")
	}
	if !strings.Contains(err.Error(), "Run 'rticloud login'") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := out.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
}

func TestParserPrintAccessTokenExchangesAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.URL.Path, "/service-accounts/auth/token"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := request.Header.Get("X-API-Key"), "service-account-key"; got != want {
			t.Errorf("X-API-Key = %q, want %q", got, want)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "service-token", "expires_in": 3600})
	}))
	defer server.Close()

	var out bytes.Buffer
	authManager := auth.New(parserAuthConfigProvider{apiHost: server.URL}, filepath.Join(t.TempDir(), "credentials.json"))
	authManager.Env = func(name string) string {
		if name == "CONNEXT_CLOUD_API_KEY" {
			return "service-account-key"
		}
		return ""
	}
	if err := Execute([]string{"print-access-token"}, &out, io.Discard, &app.Runtime{Auth: authManager}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := out.String(), "service-token\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestParserRejectsUnknownResourceWithoutCommand(t *testing.T) {
	err := Execute([]string{"databu"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected unsupported resource error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestParserRejectsVersionCommand(t *testing.T) {
	err := Execute([]string{"version"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected version command to be unsupported")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestParserVersionFlag(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"--version"}, &out, io.Discard, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "rticloud") {
		t.Fatalf("expected version output, got: %s", out.String())
	}
}

func TestParserRejectsInvalidClientKind(t *testing.T) {
	err := Execute([]string{"client", "create", "--name", "db", "--client-name", "app", "--kind", "collector"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected invalid kind error")
	}
}

func TestParserReportsMissingValuesWithoutPanic(t *testing.T) {
	err := Execute([]string{"databus", "query", "--name"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected missing value error")
	}
}

func TestParserRequiresExactlyOneObservabilityLinkAction(t *testing.T) {
	err := Execute([]string{"databus", "set-observability", "--name", "db"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected missing link action error")
	}
	err = Execute([]string{"databus", "set-observability", "--name", "db", "--service", "obs", "--unlink"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected mutually exclusive link action error")
	}
}

func TestParserRejectsInvalidLiveFormat(t *testing.T) {
	err := Execute([]string{"spy", "--format", "json"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected invalid format error")
	}
	err = Execute([]string{"gateway", "--format", "json"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected invalid format error for gateway")
	}
}

func TestParserRejectsSpyObsCommand(t *testing.T) {
	err := Execute([]string{"spy", "obs"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected unsupported spy command error")
	}
}

func TestParserEdgeProvisioningServiceHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provisioning", "service"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provisioning service help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Provisioning Services") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-provisioning service help: %s", out.String())
	}
}

func TestParserEdgeProvisioningParticipantTemplateHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provisioning", "participant-template"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provisioning participant-template help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Participant Templates") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-provisioning participant-template help: %s", out.String())
	}
}

func TestParserEdgeProvisioningCampaignHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provisioning", "campaign"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provisioning campaign help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Campaigns") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-provisioning campaign help: %s", out.String())
	}
}

func TestParserEdgeProvisioningDeviceHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provisioning", "participant"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provisioning participant help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Participants") || !strings.Contains(out.String(), "list") {
		t.Fatalf("unexpected edge-provisioning participant help: %s", out.String())
	}
}

func TestParserEdgeSyncHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-sync"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-sync help, got %v", err)
	}
	if !strings.Contains(out.String(), "Sync security artifacts") {
		t.Fatalf("unexpected edge-sync help: %s", out.String())
	}
}

func TestParserEdgeProvisioningServiceRequiresName(t *testing.T) {
	err := Execute([]string{"edge-provisioning", "service", "query"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --name required error")
	}
}

func TestParserEdgeProvisioningProfileRequiresService(t *testing.T) {
	err := Execute([]string{"edge-provisioning", "profile", "list"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --service required error")
	}
}

func TestParserEdgeSyncIdentityRequiresParticipantID(t *testing.T) {
	err := Execute([]string{"edge-sync", "identity"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --participant-id required error")
	}
}

func TestParserEdgeSyncConnextDirRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rafa")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	err := Execute([]string{"edge-sync", "identity", "--connext-dir", file}, io.Discard, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected not-a-directory error, got %v", err)
	}
}

func TestParserEdgeSyncConnextDirCreatesMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rafa")
	err := Execute([]string{"edge-sync", "identity", "--connext-dir", dir}, io.Discard, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "--participant-id") {
		t.Fatalf("expected --participant-id required error once connext-dir is valid, got %v", err)
	}
	info, statErr := os.Stat(dir)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("expected --connext-dir to be created, stat err=%v", statErr)
	}
}

func TestRootHelpShowsOperatorCommands(t *testing.T) {
	var out bytes.Buffer
	_ = Execute(nil, &out, &out, nil)
	output := out.String()
	if !strings.Contains(output, "edge-provisioning") ||
		!strings.Contains(output, "edge-sync") {
		t.Fatalf("expected operator commands in root help: %s", output)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func roundTripClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func stringResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type parserAuthConfigProvider struct {
	apiHost string
}

func (provider parserAuthConfigProvider) GetConfig() (map[string]string, error) {
	return map[string]string{"api_host": provider.apiHost}, nil
}

func (parserAuthConfigProvider) GetClientID() string                 { return "client-id" }
func (parserAuthConfigProvider) RequireConfiguration(io.Writer) bool { return true }
