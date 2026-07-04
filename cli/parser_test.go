package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
