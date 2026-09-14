// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

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
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	internalconnext "github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
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

func TestGatewayPreflightErrorShowsDatabusResetHint(t *testing.T) {
	err := gatewayPreflightError(map[string]any{"databus": "inventory", "templates": map[string]any{"gateway": "gateway"}}, true, errors.New("Databus is disabled"))
	if !strings.Contains(err.Error(), "Databus is disabled") || !strings.Contains(err.Error(), "rticloud gateway reset") {
		t.Fatalf("unexpected preflight error: %v", err)
	}
}

func TestGatewayPreflightErrorShowsResetHintForDisabledObservabilityService(t *testing.T) {
	err := gatewayPreflightError(map[string]any{"observability": "metrics", "templates": map[string]any{"collector": "collector"}}, true, errors.New("Observability Service is disabled"))
	if !strings.Contains(err.Error(), "Observability Service is disabled") || !strings.Contains(err.Error(), "rticloud gateway reset") {
		t.Fatalf("unexpected preflight error: %v", err)
	}
}

func TestGatewayPreflightErrorLeavesFirstRunErrorsUnchanged(t *testing.T) {
	original := errors.New("Gateway template was not found")
	if got := gatewayPreflightError(map[string]any{"databus": "inventory", "templates": map[string]any{"gateway": "gateway"}}, false, original); got != original {
		t.Fatalf("expected original error, got %v", got)
	}
}

func TestGatewayPreflightErrorLeavesAPIErrorsUnchanged(t *testing.T) {
	original := errors.New("API unavailable")
	if got := gatewayPreflightError(map[string]any{"databus": "inventory", "templates": map[string]any{"gateway": "gateway"}}, true, suppressResetHint(original)); got != original {
		t.Fatalf("expected original error, got %v", got)
	}
}

func TestGatewayPreflightErrorShowsResetHintForNotFoundResponse(t *testing.T) {
	_, apiErr := decodeCommandJSON(runtimeTextResponse(http.StatusNotFound, "Databus not found"), nil, "GET", "/databuses/inventory", "api.example", "gateway")
	err := gatewayPreflightError(map[string]any{"databus": "inventory", "templates": map[string]any{"gateway": "gateway"}}, true, apiErr)
	if !strings.Contains(err.Error(), "Databus not found") || !strings.Contains(err.Error(), "rticloud gateway reset") {
		t.Fatalf("unexpected preflight error: %v", err)
	}
}

func TestGatewayPreflightErrorSuppressesResetHintForServerError(t *testing.T) {
	_, apiErr := decodeCommandJSON(runtimeTextResponse(http.StatusServiceUnavailable, "temporarily unavailable"), nil, "GET", "/databuses/inventory", "api.example", "gateway")
	err := gatewayPreflightError(map[string]any{"databus": "inventory", "templates": map[string]any{"gateway": "gateway"}}, true, apiErr)
	if !strings.Contains(err.Error(), "temporarily unavailable") || strings.Contains(err.Error(), "rticloud gateway reset") {
		t.Fatalf("unexpected preflight error: %v", err)
	}
}

func TestSpyPreflightErrorShowsDatabusResetHint(t *testing.T) {
	err := spyPreflightError(map[string]any{"databus": "inventory", "templates": map[string]any{"app": "rticloud_spy"}}, true, errors.New("Cloud Native application was not found"))
	if !strings.Contains(err.Error(), "Cloud Native application was not found") || !strings.Contains(err.Error(), "rticloud spy reset") {
		t.Fatalf("unexpected preflight error: %v", err)
	}
}

func TestSpyPreflightErrorLeavesFirstRunErrorsUnchanged(t *testing.T) {
	original := errors.New("Cloud Native application was not found")
	if got := spyPreflightError(map[string]any{"databus": "inventory", "templates": map[string]any{"app": "rticloud_spy"}}, false, original); got != original {
		t.Fatalf("expected original error, got %v", got)
	}
}

func TestSpyPreflightErrorLeavesAPIErrorsUnchanged(t *testing.T) {
	original := errors.New("API unavailable")
	if got := spyPreflightError(map[string]any{"databus": "inventory", "templates": map[string]any{"app": "rticloud_spy"}}, true, suppressResetHint(original)); got != original {
		t.Fatalf("expected original error, got %v", got)
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
		if !strings.Contains(message, install) || len(choices) != 3 || choices[0] != downloadConnextLicenseLabel || choices[1] != manualDownloadConnextLicenseLabel {
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

func TestEnsureConnextLicenseShowsManualDownloadInstructions(t *testing.T) {
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
	licensePath := filepath.Join(install, "rti_license.dat")
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string) (string, error) {
		if !strings.Contains(message, install) || len(choices) != 3 || choices[1] != manualDownloadConnextLicenseLabel {
			t.Fatalf("unexpected prompt: %q %#v", message, choices)
		}
		return manualDownloadConnextLicenseLabel, nil
	})
	if err == nil || !strings.Contains(err.Error(), "Copy the license file to "+licensePath) {
		t.Fatalf("unexpected error: %v", err)
	}
	if api.calls != 0 {
		t.Fatalf("expected no API calls, got %d", api.calls)
	}
	rendered := out.String()
	checks := []string{
		"Manually download Connext license",
		"Step 1",
		evaluationLicenseURL,
		"Step 2",
		"Download the license file.",
		"Step 3",
		"Copy it to " + licensePath,
	}
	for _, check := range checks {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing %q in output: %s", check, rendered)
		}
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
		if !strings.Contains(message, install) || len(choices) != 3 || choices[2] != cancelConnextLicenseLabel {
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

func (api *runtimeFakeAPI) PostWithBearerToken(path string, payload any, _ string) (*http.Response, error) {
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

func TestReturningProjectIgnoresSavedConnextPath(t *testing.T) {
	t.Setenv("NDDSHOME", "")
	old := internalconnext.ManagedInstaller
	t.Cleanup(func() { internalconnext.ManagedInstaller = old })
	internalconnext.ManagedInstaller = func(options internalconnext.DiscoveryOptions) (internalconnext.Install, error) {
		return internalconnext.Install{Path: "current-managed", Version: "7.7.0.1"}, nil
	}
	values := map[string]any{"runtime": map[string]any{"connext_home": "obsolete-installation"}}
	for _, skip := range []bool{false, true} {
		result, err := resolveRuntimeConnext(values, true, skip, internalconnext.ConfirmationFromSelector(func(string, []string) (string, error) { t.Fatal("unexpected selector"); return "", nil }), internalconnext.DiscoveryOptions{})
		if err != nil || result.Path != "current-managed" {
			t.Fatalf("%#v %v", result, err)
		}
	}
}

func TestRuntimeNDDSHOMEConfirmationAndSkipPreflight(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(filepath.Join(directory, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(internalconnext.Executable(directory, "rtiroutingservice"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NDDSHOME", directory)
	for _, skip := range []bool{false, true} {
		calls := 0
		result, err := resolveRuntimeConnext(nil, true, skip, internalconnext.ConfirmationFromSelector(func(string, []string) (string, error) { calls++; return internalconnext.UseNDDSHOMELabel, nil }), internalconnext.DiscoveryOptions{})
		if err != nil || result.Path != directory {
			t.Fatalf("%#v %v", result, err)
		}
		if (skip && calls != 0) || (!skip && calls != 1) {
			t.Fatalf("skip=%v prompts=%d", skip, calls)
		}
	}
	// Initial setup has already asked, so do not ask again before launch.
	values := map[string]any{"runtime": map[string]any{"connext_home": directory}}
	result, err := resolveRuntimeConnext(values, false, false, internalconnext.ConfirmationFromSelector(func(string, []string) (string, error) { t.Fatal("duplicate confirmation"); return "", nil }), internalconnext.DiscoveryOptions{})
	if err != nil || result.Path != directory {
		t.Fatalf("%#v %v", result, err)
	}
}

func TestLogoutAttemptsBothStoresOnFailure(t *testing.T) {
	invalid := filepath.Join(t.TempDir(), "not-a-token")
	if err := os.MkdirAll(invalid, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, "keep"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	valid := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(valid, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{Auth: auth.New(nil, invalid), WorkAuth: auth.New(nil, valid)}
	if err := runtime.Logout(); err == nil {
		t.Fatal("expected first store error")
	}
	if _, err := os.Stat(valid); !os.IsNotExist(err) {
		t.Fatal("second store was not cleared")
	}
}
