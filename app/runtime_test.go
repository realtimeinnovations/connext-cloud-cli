// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package app

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
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
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	internalconnext "github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/update"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

const runtimeValidConnextLicense = "FEATURE RTIPRO RTI 4. permanent 0 SIGN=TEST_ONLY\n" +
	"FEATURE RTISECURITY RTI 5. permanent 0 SIGN=TEST_ONLY\n" +
	"FEATURE RTIRWT RTI 6. permanent 0 SIGN=TEST_ONLY\n"

func TestRunUpdateRequiresManager(t *testing.T) {
	for _, runtime := range []*Runtime{nil, {}} {
		err := runtime.RunUpdate(context.Background(), io.Discard, io.Discard, update.Options{})
		if err == nil || err.Error() != "update manager is not configured" || clierror.From(err).Code != clierror.CodeCommandFailed {
			t.Fatalf("expected operational configuration error, got %v", err)
		}
	}
}

func TestConfigureConnextDirCreatesAndSelectsDirectory(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{EdgeStore: edgestore.New(base)}
	path := filepath.Join(base, "artifacts")
	for attempt := 0; attempt < 2; attempt++ {
		if err := runtime.ConfigureConnextDir(path); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() || runtime.EdgeStore.ConnextDir != path {
		t.Fatalf("directory not created or selected: err=%v selected=%q", err, runtime.EdgeStore.ConnextDir)
	}
}

func TestConfigureConnextDirClassifiesErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var runtime *Runtime
	err := runtime.ConfigureConnextDir(path)
	if err == nil || clierror.From(err).Code != clierror.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for a regular file, got %v", err)
	}
	err = runtime.ConfigureConnextDir(filepath.Join(path, "child"))
	var cause *os.PathError
	if !errors.As(err, &cause) || !strings.Contains(err.Error(), "--connext-dir") || clierror.From(err).Code != clierror.CodeCommandFailed {
		t.Fatalf("expected wrapped filesystem failure, got %v", err)
	}
}

func TestConfigureConnextDirAcceptsSelectedDirectorySymlink(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(base, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	runtime := &Runtime{EdgeStore: edgestore.New(base)}
	for _, path := range []string{link, filepath.Join(link, "new")} {
		if err := runtime.ConfigureConnextDir(path); err != nil {
			t.Fatal(err)
		}
		if err := runtime.EdgeStore.WriteFile(filepath.Join(runtime.EdgeStore.ConnextDir, "artifact"), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(path, "artifact"))
		if err != nil || string(data) != "test" {
			t.Fatalf("selected directory output=%q, %v", data, err)
		}
	}

}

func TestDecodeGatewayJSONPassesThroughNotConfiguredError(t *testing.T) {
	_, err := decodeCommandJSON(nil, config.ErrNotConfigured, "GET", "/databuses?extra_fields=true", "", "gateway")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != config.NotConfiguredMessage {
		t.Fatalf("unexpected error message: %s", err)
	}
	if !errors.Is(err, config.ErrNotConfigured) {
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

func managedLicenseRuntime(t *testing.T) (*Runtime, internalconnext.Install, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldHome, oldPlatform := internalconnext.UserHomeDir, internalconnext.Platform
	internalconnext.UserHomeDir = func() (string, error) { return home, nil }
	internalconnext.Platform = func() (string, string) { return "linux", "amd64" }
	t.Cleanup(func() { internalconnext.UserHomeDir, internalconnext.Platform = oldHome, oldPlatform })
	t.Setenv("NDDSHOME", "")
	t.Setenv("RTI_LICENSE_FILE", "")
	path, err := internalconnext.ManagedInstallationPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	return &Runtime{Out: io.Discard}, internalconnext.Install{Path: path, Version: "7.7.0.1"}, filepath.Join(home, ".rti", "rticloud", "licenses", "rti_license.dat")
}

// licensePrompts uses each application's real adapters, including the shared
// buffered reader that carries piped answers from selection to text input.
func licensePrompts(t *testing.T, command, answers string, out io.Writer) (prompt.SelectFunc, func(string) (string, error)) {
	t.Helper()
	if command == "gateway" {
		application := gateway.NewGatewayApp(t.TempDir(), out)
		application.In = strings.NewReader(answers)
		return application.SelectFunc, application.InputFunc
	}
	application := spy.NewApp(t.TempDir(), out)
	application.In = strings.NewReader(answers)
	return application.SelectFunc, application.InputFunc
}

func TestLicenseAcquisitionWithPipedAnswers(t *testing.T) {
	const incomplete = "FEATURE RTIPRO RTI 4. permanent SECRET_LICENSE\n"
	const expired = "FEATURE RTIPRO RTI 4. 01-jan-2000 SECRET_LICENSE\n"
	const entered = "2\n~/chosen license.dat\n"
	for _, command := range []string{"gateway", "spy"} {
		for _, scenario := range []struct {
			name, answers, source, installed, download, want, errorText string
			order                                                       []string
			external                                                    bool
			status, calls                                               int
		}{
			{name: "valid-enter", answers: entered + "\n", want: runtimeValidConnextLicense, order: []string{"Path to Connext license file", "License feature check passed:", "Connext license saved to"}},
			{name: "valid-eof", answers: entered, errorText: "cancelled"},
			{name: "decline-copy", answers: entered + "2\n4\n", errorText: "cancelled", order: []string{"License feature check passed:", "Choose how to provide a Connext license:"}},
			{name: "warnings-enter", source: incomplete, answers: entered + "\n", errorText: "precheck declined"},
			{name: "warnings-eof", source: incomplete, answers: entered, errorText: "cancelled"},
			{name: "warnings-copy-default", source: incomplete, answers: entered + "2\n\n4\n", errorText: "cancelled", order: []string{"Proceed anyway?", "License feature warnings were accepted.", "Choose how to provide a Connext license:"}},
			{name: "warnings-copy-eof", source: incomplete, answers: entered + "2\n", errorText: "cancelled"},
			{name: "warnings-approved", source: incomplete, answers: entered + "2\n2\n", want: incomplete, order: []string{"Proceed anyway?", "License feature warnings were accepted.", "Connext license saved to"}},
			{name: "correct-path", answers: "2\n~/missing.dat\n~/chosen license.dat\n\n", want: runtimeValidConnextLicense, order: []string{"license file cannot be read", "Path to Connext license file", "License feature check passed:", "Connext license saved to"}},
			{name: "correct-and-replace", source: incomplete, answers: "2\n~/missing.dat\n~/chosen license.dat\n3\n2\n~/replacement.dat\n\n", want: runtimeValidConnextLicense, order: []string{"license file cannot be read", "Proceed anyway?", "Choose how to provide a Connext license:", "License feature check passed:", "Connext license saved to"}},
			{name: "discovered-replace-path", installed: expired, answers: "3\n" + entered + "\n", want: runtimeValidConnextLicense, order: []string{"Proceed anyway?", "Choose how to provide a Connext license:", "License feature check passed:", "Connext license saved to"}},
			{name: "discovered-replace-download", installed: expired, answers: "3\n1\n", want: runtimeValidConnextLicense, calls: 1},
			{name: "discovered-decline", installed: expired, answers: "\n", errorText: "precheck declined"},
			{name: "discovered-replacement-eof", installed: expired, answers: "3\n" + entered, errorText: "cancelled"},
			{name: "download", answers: "1\n", want: runtimeValidConnextLicense, calls: 1},
			{name: "download-decline-warnings", download: incomplete, answers: "1\n\n", errorText: "precheck declined", calls: 1},
			{name: "download-replaces-only-after-approval", installed: expired, download: incomplete, answers: "3\n1\n\n", errorText: "precheck declined", calls: 1},
			{name: "manual", answers: "3\n", errorText: "Copy the license file to", order: []string{"Manually download Connext license", "Step 1", evaluationLicenseURL, "Step 2", "Download the license file.", "Step 3"}},
			{name: "cancel-before-login", answers: "4\n", errorText: "cancelled"},
			{name: "external-lm-download", external: true, answers: "1\n", want: runtimeValidConnextLicense, calls: 1},
			{name: "external-lm-manual", external: true, answers: "3\n", errorText: "Copy the license file to", order: []string{evaluationLicenseURL, "Download the license file.", "Copy the license file to"}},
			{name: "external-lm-cancel", external: true, answers: "4\n", errorText: "cancelled"},
			{name: "download-error", answers: "1\n", status: http.StatusInternalServerError, download: `{"error":"Unexpected error"}`, errorText: "Connext license download failed", calls: 1},
		} {
			t.Run(command+"/"+scenario.name, func(t *testing.T) {
				runtime, install, saved := managedLicenseRuntime(t)
				if scenario.external {
					install = internalconnext.Install{Path: t.TempDir(), Version: "7.7.0"}
					if err := os.WriteFile(filepath.Join(install.Path, "rti_versions.xml"), []byte("<rti><host><installation_type>LM</installation_type></host></rti>"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				var out bytes.Buffer
				runtime.Out = &out
				home, err := internalconnext.UserHomeDir()
				if err != nil {
					t.Fatal(err)
				}
				source := scenario.source
				if source == "" {
					source = runtimeValidConnextLicense
				}
				for name, contents := range map[string]string{"chosen license.dat": source, "replacement.dat": runtimeValidConnextLicense} {
					if err := os.WriteFile(filepath.Join(home, name), []byte(contents), 0600); err != nil {
						t.Fatal(err)
					}
				}
				local := internalconnext.LicenseFilePath(install)
				if scenario.installed != "" {
					if err := os.WriteFile(local, []byte(scenario.installed), 0600); err != nil {
						t.Fatal(err)
					}
				}
				download := scenario.download
				if download == "" {
					download = runtimeValidConnextLicense
				}
				status := scenario.status
				if status == 0 {
					status = http.StatusOK
				}
				api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(status, download)}}
				runtime.License = commands.New(api, &out)
				selectFunc, inputFunc := licensePrompts(t, command, scenario.answers, &out)
				err = runtime.ensureConnextLicense(install, selectFunc, inputFunc)
				if scenario.errorText == "" {
					if err != nil {
						t.Fatalf("unexpected failure: %v\n%s", err, out.String())
					}
				} else if err == nil || !strings.Contains(err.Error(), scenario.errorText) {
					t.Fatalf("error=%v, want %q\n%s", err, scenario.errorText, out.String())
				}
				if scenario.status != 0 {
					var cause *httputil.StatusError
					if !errors.As(err, &cause) || cause.StatusCode != scenario.status || clierror.From(err).Code != clierror.CodeAPIError || !strings.Contains(err.Error(), "Unexpected error") {
						t.Fatalf("download failure lost its HTTP cause: %v", err)
					}
				}
				if api.calls != scenario.calls {
					t.Fatalf("download calls=%d, want %d", api.calls, scenario.calls)
				}
				remaining := out.String()
				for _, fragment := range scenario.order {
					index := strings.Index(remaining, fragment)
					if index < 0 {
						t.Fatalf("missing or out-of-order %q\n%s", fragment, out.String())
					}
					remaining = remaining[index+len(fragment):]
				}
				if strings.Contains(out.String(), "SECRET_LICENSE") {
					t.Fatal("license contents leaked into prompts")
				}
				if scenario.errorText == "Copy the license file to" && (!strings.Contains(err.Error(), local) || !strings.Contains(out.String(), local)) {
					t.Fatalf("manual instructions omitted destination %s: %v\n%s", local, err, out.String())
				}
				for _, path := range []string{saved, local} {
					want := scenario.want
					if path == saved && scenario.external {
						want = ""
					} else if path == local && want == "" {
						want = scenario.installed
					}
					data, readErr := os.ReadFile(path)
					if want == "" {
						if !os.IsNotExist(readErr) {
							t.Fatalf("unapproved license saved at %s: %v", path, readErr)
						}
					} else if readErr != nil || string(data) != want {
						t.Fatalf("license at %s changed unexpectedly: %v", path, readErr)
					}
				}
				if strings.Contains(out.String(), "This license file will be copied to:") {
					if !strings.Contains(out.String(), filepath.Join(home, "chosen license.dat")) || !strings.Contains(out.String(), saved) {
						t.Fatalf("copy confirmation omitted source or destination: %s", out.String())
					}
				}
				if data, err := os.ReadFile(filepath.Join(home, "chosen license.dat")); err != nil || string(data) != source {
					t.Fatalf("source license changed: %v", err)
				}
				if scenario.want != "" {
					if !strings.Contains(out.String(), "Connext license saved to "+local) {
						t.Fatalf("save result omitted destination: %s", out.String())
					}
					before := out.Len()
					if err := runtime.ensureConnextLicense(install, nil, nil); err != nil || out.Len() != before || api.calls != scenario.calls {
						t.Fatalf("unchanged license required more work: %v", err)
					}
				}
			})
		}
	}
}

func TestEnsureConnextLicenseUsesEnvironmentDirectly(t *testing.T) {
	for _, scenario := range []string{"complete", "decline-incomplete", "override-incomplete", "noninteractive-incomplete", "missing-file", "external-install", "relative-file", "quoted-file", "symlink-file"} {
		t.Run(scenario, func(t *testing.T) {
			runtime, install, shared := managedLicenseRuntime(t)
			var out bytes.Buffer
			runtime.Out = &out
			stored := runtimeValidConnextLicense + "# saved license\n"
			local := runtimeValidConnextLicense + "# installation copy\n"
			if err := os.MkdirAll(filepath.Dir(shared), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shared, []byte(stored), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(internalconnext.LicenseFilePath(install), []byte(local), 0600); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(t.TempDir(), "environment.dat")
			contents := runtimeValidConnextLicense
			if strings.HasSuffix(scenario, "incomplete") {
				contents = "FEATURE RTIPRO RTI 4. permanent SECRET_LICENSE"
			}
			if scenario != "missing-file" {
				if err := os.WriteFile(source, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			environmentPath := source
			if scenario == "symlink-file" {
				environmentPath = filepath.Join(t.TempDir(), "current-license.dat")
				if err := os.Symlink(source, environmentPath); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				source = environmentPath
			} else if scenario == "relative-file" {
				t.Chdir(filepath.Dir(source))
				environmentPath = filepath.Base(source)
			} else if scenario == "quoted-file" {
				environmentPath = "\"" + source + "\""
			}
			t.Setenv("RTI_LICENSE_FILE", environmentPath)
			selected := install
			if scenario == "external-install" {
				selected = internalconnext.Install{Path: t.TempDir()}
			}
			answers := "\n\n"
			if scenario == "override-incomplete" {
				answers = "2\n2\n"
			}
			selectFunc, _ := licensePrompts(t, "gateway", answers, &out)
			if scenario == "noninteractive-incomplete" {
				selectFunc = nil
			}
			for attempt := 0; attempt < 2; attempt++ {
				err := runtime.ensureConnextLicense(selected, selectFunc, nil)
				wantSuccess := scenario == "complete" || scenario == "external-install" || scenario == "override-incomplete" || scenario == "relative-file" || scenario == "quoted-file" || scenario == "symlink-file"
				if (err == nil) != wantSuccess {
					t.Fatalf("unexpected environment precheck: %v", err)
				}
				if scenario == "noninteractive-incomplete" && clierror.From(err).Code != clierror.CodeInputRequired {
					t.Fatalf("expected input-required error, got %v", err)
				}
				if err != nil && !strings.Contains(err.Error(), "Unset RTI_LICENSE_FILE") {
					t.Fatalf("missing override guidance: %v", err)
				}
			}
			wantPrompts := 0
			if scenario == "decline-incomplete" || scenario == "override-incomplete" {
				wantPrompts = 2
			}
			prompts := strings.Count(out.String(), "Proceed anyway?")
			if prompts != wantPrompts || !strings.Contains(out.String(), "found via RTI_LICENSE_FILE") || !strings.Contains(out.String(), source) || !strings.Contains(out.String(), "Unset RTI_LICENSE_FILE") {
				t.Fatalf("missing repeated precheck or source guidance: prompts=%d output=%s", prompts, out.String())
			}
			if strings.Contains(out.String(), "SECRET_LICENSE") || strings.Contains(out.String(), "This license file will be copied") || strings.Contains(out.String(), "Choose how to provide") {
				t.Fatal("environment override leaked contents or offered license copying")
			}
			if !strings.Contains(strings.Join(selected.EnvironmentOverrides(), "\n"), "RTI_LICENSE_FILE="+source) || os.Getenv("RTI_LICENSE_FILE") != environmentPath {
				t.Fatal("child must use the checked absolute path without changing the parent environment")
			}
			for path, want := range map[string]string{shared: stored, internalconnext.LicenseFilePath(install): local} {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != want {
					t.Fatalf("environment selection changed saved license %s: %v", path, err)
				}
			}
		})
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
	api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, runtimeValidConnextLicense)}}
	runtime := &Runtime{License: commands.New(api, io.Discard)}
	if err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string, _ string) (string, error) {
		t.Fatalf("did not expect prompt for non-LM install: %s %#v", message, choices)
		return "", nil
	}, nil); err != nil {
		t.Fatal(err)
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
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
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
	_, install, _ := managedLicenseRuntime(t)
	if err := os.MkdirAll(filepath.Join(install.Path, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"rtiroutingservice", "rtiddsspy", "rticollectorservicelite"} {
		if err := os.WriteFile(internalconnext.Executable(install.Path, tool), []byte("test executable"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	metadata := `<rti><host><base_version>` + install.Version + `</base_version><install_dirname>` + filepath.Base(install.Path) + `</install_dirname></host></rti>`
	if err := os.WriteFile(filepath.Join(install.Path, "rti_versions.xml"), []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{"runtime": map[string]any{"connext_home": "obsolete-installation"}}
	for _, skip := range []bool{false, true} {
		var out bytes.Buffer
		selectFunc, _ := licensePrompts(t, "gateway", "", &out)
		result, err := resolveRuntimeConnext(values, true, skip, internalconnext.ConfirmationFromSelector(selectFunc), internalconnext.DiscoveryOptions{Output: &out})
		if err != nil || result.Path != install.Path || result.Version != install.Version || out.Len() != 0 {
			t.Fatalf("skip=%v install=%#v error=%v output=%s", skip, result, err, out.String())
		}
	}
}

func TestRuntimeNDDSHOMEConfirmationAndSkipPreflight(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(filepath.Join(directory, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(internalconnext.Executable(directory, "rtiroutingservice"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NDDSHOME", directory)
	for _, skip := range []bool{false, true} {
		calls := 0
		result, err := resolveRuntimeConnext(nil, true, skip, internalconnext.ConfirmationFromSelector(func(string, []string, string) (string, error) {
			calls++
			return internalconnext.UseNDDSHOMELabel, nil
		}), internalconnext.DiscoveryOptions{})
		if err != nil || result.Path != directory {
			t.Fatalf("%#v %v", result, err)
		}
		if (skip && calls != 0) || (!skip && calls != 1) {
			t.Fatalf("skip=%v prompts=%d", skip, calls)
		}
	}
	// Initial setup has already asked, so do not ask again before launch.
	selected := filepath.Join(t.TempDir(), "selected-install")
	if err := os.Symlink(directory, selected); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	values := map[string]any{"runtime": map[string]any{"connext_home": selected}}
	result, err := resolveRuntimeConnext(values, false, false, internalconnext.ConfirmationFromSelector(func(string, []string, string) (string, error) {
		t.Fatal("duplicate confirmation")
		return "", nil
	}), internalconnext.DiscoveryOptions{})
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

func TestAPIAdaptersAndPresentationPreserveErrorCategories(t *testing.T) {
	for _, command := range []string{"gateway", "spy"} {
		present := gatewayPreflightError
		if command == "spy" {
			present = spyPreflightError
		}
		project := map[string]any{"databus": "demo", "templates": map[string]any{"gateway": "gw", "app": "spy"}}
		authErr := clierror.AuthRequired()
		_, adapted := decodeCommandJSON(nil, authErr, "GET", "/databuses/demo", "https://api.example.test", command)
		err := present(project, true, adapted)
		if !errors.Is(err, authErr) || clierror.From(err).ExitCode() != 3 || strings.Contains(err.Error(), "Cannot reach") || strings.Contains(err.Error(), "reset") {
			t.Fatalf("%s authentication error changed: %v", command, err)
		}
		for _, status := range []int{401, 403, 404} {
			_, adapted := decodeCommandJSON(runtimeTextResponse(status, `{"message":"rejected"}`), nil, "GET", "/databuses/demo", "https://api.example.test", command)
			err := present(project, true, adapted)
			var cause *httputil.StatusError
			expectedExit := 3
			if status == 404 {
				expectedExit = 4
			}
			if !errors.As(err, &cause) || cause.StatusCode != status || clierror.From(err).ExitCode() != expectedExit {
				t.Fatalf("%s HTTP %d changed: %v", command, status, err)
			}
		}
		// Even when presentation appends a reset hint, the typed cause survives.
		err = present(project, true, authErr)
		if !errors.Is(err, authErr) || clierror.From(err).ExitCode() != 3 {
			t.Fatalf("%s presentation lost authentication: %v", command, err)
		}
	}
}

func TestAgentIdentityGenerationAndRenewalThroughTemporaryBaseSymlink(t *testing.T) {
	base := t.TempDir()
	selected := filepath.Join(t.TempDir(), "temporary")
	if err := os.Symlink(base, selected); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	keyPath, csrPath, err := generateAgentKeyAndCSR("initial", "org", selected)
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	readCSR := func(path string) *x509.CertificateRequest {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(data)
		if block == nil {
			t.Fatal("missing CSR PEM")
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if err := csr.CheckSignature(); err != nil {
			t.Fatal(err)
		}
		return csr
	}
	initial := readCSR(csrPath)
	if initial.Subject.CommonName != "initial" {
		t.Fatal("wrong initial subject")
	}
	csrPath, err = generateAgentCSRFromKey("renewed", "org", key, selected)
	if err != nil {
		t.Fatal(err)
	}
	renewed := readCSR(csrPath)
	if renewed.Subject.CommonName != "renewed" || !bytes.Equal(initial.RawSubjectPublicKeyInfo, renewed.RawSubjectPublicKeyInfo) {
		t.Fatal("renewal changed key or used wrong subject")
	}
	stored, err := os.ReadFile(filepath.Join(base, "identity.key"))
	if err != nil || !bytes.Equal(stored, key) {
		t.Fatal("key did not reach selected base or was changed")
	}
}
