package app

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/commands"
	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	internalconnext "github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

func TestDecodeGatewayJSONPassesThroughNotConfiguredError(t *testing.T) {
	_, err := decodeCommandJSON(nil, config.ErrNotConfigured, "GET", "/databuses?extra_fields=true", "", "gateway")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != config.NotConfiguredMessage {
		t.Fatalf("unexpected error message: %s", err)
	}
	if !errors.Is(config.ErrNotConfigured, config.ErrNotConfigured) {
		t.Fatal("expected sentinel error")
	}
}

func TestRuntimeLogoutRemovesCloudAndWorkspacesCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	cloudPath := filepath.Join(tmpDir, "credentials.json")
	workspacesPath := filepath.Join(tmpDir, "workspaces_credentials.json")
	if err := os.WriteFile(cloudPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspacesPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{
		Auth:     auth.New(nil, cloudPath),
		WorkAuth: auth.New(nil, workspacesPath),
	}
	if err := runtime.Logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cloudPath); !os.IsNotExist(err) {
		t.Fatalf("expected cloud credentials removed, got %v", err)
	}
	if _, err := os.Stat(workspacesPath); !os.IsNotExist(err) {
		t.Fatalf("expected workspaces credentials removed, got %v", err)
	}
}

func TestRunSpyPrintsServiceStatusCheckNoticeForExistingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	spyApp := spy.NewApp(tmpDir, &out)
	if err := spyApp.WriteConfig(map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}}); err != nil {
		t.Fatal(err)
	}
	spyApp.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": name, "status": common.ServiceStatusDisabled, "clients": map[string]any{"app_1": map[string]any{"kind": "app"}}}, nil
	}
	runtime := &Runtime{Out: &out, Spy: spyApp}
	err := runtime.RunSpy("", false)
	if err == nil {
		t.Fatal("expected inactive service error")
	}
	if !strings.Contains(out.String(), "\x1b[38;5;110m•\x1b[0m Checking service status.") {
		t.Fatalf("expected service status notice, got: %s", out.String())
	}
}

func TestRunGatewayPrintsServiceStatusCheckNoticeForExistingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	gatewayApp := gateway.NewGatewayApp(tmpDir, &out)
	if err := gatewayApp.WriteConfig(map[string]any{"databus": "db", "templates": map[string]any{"gateway": "gw"}}); err != nil {
		t.Fatal(err)
	}
	gatewayApp.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": name, "status": common.ServiceStatusDisabled, "clients": map[string]any{"gw": map[string]any{"kind": "gateway"}}}, nil
	}
	runtime := &Runtime{Out: &out, Gateway: gatewayApp}
	err := runtime.RunGateway("", false)
	if err == nil {
		t.Fatal("expected inactive service error")
	}
	if !strings.Contains(out.String(), "\x1b[38;5;110m•\x1b[0m Checking service status.") {
		t.Fatalf("expected service status notice, got: %s", out.String())
	}
}

func TestEnsureConnextLicenseDownloadsMissingLMInstallLicense(t *testing.T) {
	install := filepath.Join(t.TempDir(), "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "rti_versions.xml"), []byte(`
<rti>
  <host>
    <installation_type>LM</installation_type>
  </host>
</rti>`), 0o644); err != nil {
		t.Fatal(err)
	}
	api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, "license-body")}}
	var out bytes.Buffer
	runtime := &Runtime{Out: &out, License: commands.New(api, &out)}
	prompted := false
	if err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string) (string, error) {
		prompted = true
		if !strings.Contains(message, install) || len(choices) != 2 || choices[0] != downloadConnextLicenseLabel {
			t.Fatalf("unexpected prompt: %q %#v", message, choices)
		}
		return downloadConnextLicenseLabel, nil
	}); err != nil {
		t.Fatal(err)
	}
	if !prompted {
		t.Fatal("expected license download prompt")
	}
	data, err := os.ReadFile(filepath.Join(install, "rti_license.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "license-body" {
		t.Fatalf("unexpected license content: %s", data)
	}
	if api.calls != 1 {
		t.Fatalf("expected one API call, got %d", api.calls)
	}
	if !strings.Contains(out.String(), "Connext license saved to "+filepath.Join(install, "rti_license.dat")) {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestEnsureConnextLicenseSkipsNonLMInstall(t *testing.T) {
	install := filepath.Join(t.TempDir(), "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "rti_versions.xml"), []byte(`
<rti>
  <host>
    <installation_type>HOST</installation_type>
  </host>
</rti>`), 0o644); err != nil {
		t.Fatal(err)
	}
	api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, "license-body")}}
	runtime := &Runtime{License: commands.New(api, io.Discard)}
	if err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string) (string, error) {
		t.Fatalf("did not expect prompt for non-LM install: %s %#v", message, choices)
		return "", nil
	}); err != nil {
		t.Fatal(err)
	}
	if api.calls != 0 {
		t.Fatalf("expected no API calls, got %d", api.calls)
	}
}

func TestEnsureConnextLicenseCanBeCancelledBeforeEvaluationLogin(t *testing.T) {
	install := filepath.Join(t.TempDir(), "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "rti_versions.xml"), []byte(`
<rti>
  <host>
    <installation_type>LM</installation_type>
  </host>
</rti>`), 0o644); err != nil {
		t.Fatal(err)
	}
	api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, "license-body")}}
	runtime := &Runtime{License: commands.New(api, io.Discard)}
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string) (string, error) {
		if !strings.Contains(message, install) || len(choices) != 2 || choices[1] != cancelConnextLicenseLabel {
			t.Fatalf("unexpected prompt: %q %#v", message, choices)
		}
		return cancelConnextLicenseLabel, nil
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("unexpected error: %v", err)
	}
	if api.calls != 0 {
		t.Fatalf("expected no API calls, got %d", api.calls)
	}
}

func TestEnsureConnextLicenseRequiresInteractivePrompt(t *testing.T) {
	install := filepath.Join(t.TempDir(), "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "rti_versions.xml"), []byte(`
<rti>
  <host>
    <installation_type>LM</installation_type>
  </host>
</rti>`), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{License: commands.New(&runtimeFakeAPI{}, io.Discard)}
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, nil)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureConnextLicenseAddsContextToDownloadError(t *testing.T) {
	install := filepath.Join(t.TempDir(), "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(install, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "rti_versions.xml"), []byte(`
<rti>
  <host>
    <installation_type>LM</installation_type>
  </host>
</rti>`), 0o644); err != nil {
		t.Fatal(err)
	}
	api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusInternalServerError, `{"error":"Unexpected error"}`)}}
	runtime := &Runtime{License: commands.New(api, io.Discard)}
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string) (string, error) {
		return downloadConnextLicenseLabel, nil
	})
	if err == nil || !strings.Contains(err.Error(), "Connext license download failed") || !strings.Contains(err.Error(), "Unexpected error") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type runtimeFakeAPI struct {
	responses map[string]*http.Response
	calls     int
}

func (api *runtimeFakeAPI) Get(path string) (*http.Response, error) {
	return api.response("GET", path), nil
}

func (api *runtimeFakeAPI) Post(path string, payload any) (*http.Response, error) {
	api.calls++
	return api.response("POST", path), nil
}

func (api *runtimeFakeAPI) Patch(path string, payload any) (*http.Response, error) {
	return api.response("PATCH", path), nil
}

func (api *runtimeFakeAPI) Delete(path string) (*http.Response, error) {
	return api.response("DELETE", path), nil
}

func (api *runtimeFakeAPI) response(method string, path string) *http.Response {
	if response := api.responses[method+" "+path]; response != nil {
		return response
	}
	return runtimeTextResponse(http.StatusNotFound, "not found")
}

func runtimeTextResponse(statusCode int, body string) *http.Response {
	return &http.Response{StatusCode: statusCode, Status: http.StatusText(statusCode), Body: io.NopCloser(strings.NewReader(body))}
}
