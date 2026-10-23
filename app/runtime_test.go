// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package app

import (
	"bytes"
	"context"
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
	base := t.TempDir()
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

func TestConfigureConnextDirRejectsLinkedDirectory(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(base, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	runtime := &Runtime{EdgeStore: edgestore.New(base)}
	original := runtime.EdgeStore.ConnextDir
	for _, path := range []string{link, filepath.Join(link, "new")} {
		if err := runtime.ConfigureConnextDir(path); err == nil {
			t.Fatalf("accepted linked destination %s", path)
		}
		if runtime.EdgeStore.ConnextDir != original {
			t.Fatal("rejected destination changed the selected directory")
		}
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected destination changed external directory: %v", err)
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
	api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, runtimeValidConnextLicense)}}
	var out bytes.Buffer
	runtime := &Runtime{Out: &out, License: commands.New(api, &out)}
	prompted := false
	if err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string, _ string) (string, error) {
		prompted = true
		if !strings.Contains(message, install) || len(choices) != 4 || choices[0] != downloadConnextLicenseLabel || choices[1] != enterConnextLicensePathLabel || choices[2] != manualDownloadConnextLicenseLabel {
			t.Fatalf("unexpected prompt: %q %#v", message, choices)
		}
		return downloadConnextLicenseLabel, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	if !prompted {
		t.Fatal("expected license download prompt")
	}
	data, err := os.ReadFile(filepath.Join(install, "rti_license.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != runtimeValidConnextLicense {
		t.Fatalf("unexpected license content: %s", data)
	}
	if api.calls != 1 {
		t.Fatalf("expected one API call, got %d", api.calls)
	}
	if !strings.Contains(out.String(), "Connext license saved to "+filepath.Join(install, "rti_license.dat")) {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func managedLicenseRuntime(t *testing.T) (*Runtime, internalconnext.Install, string) {
	t.Helper()
	home := t.TempDir()
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

func TestLicenseAcquisitionWithPipedAnswers(t *testing.T) {
	for _, command := range []string{"gateway", "spy"} {
		for _, scenario := range []string{"valid-enter", "valid-eof", "warnings-enter", "warnings-copy-default", "correct-and-replace"} {
			t.Run(command+"/"+scenario, func(t *testing.T) {
				runtime, install, saved := managedLicenseRuntime(t)
				var out bytes.Buffer
				runtime.Out = &out
				source := filepath.Join(t.TempDir(), "chosen.dat")
				contents := runtimeValidConnextLicense
				if strings.HasPrefix(scenario, "warnings") || scenario == "correct-and-replace" {
					contents = "FEATURE RTIPRO RTI 4. permanent SIGN=TEST_ONLY\n"
				}
				if err := os.WriteFile(source, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
				answers := "2\n" + source + "\n"
				success := scenario == "valid-enter" || scenario == "correct-and-replace"
				switch scenario {
				case "valid-enter", "warnings-enter":
					answers += "\n"
				case "warnings-copy-default":
					answers += "2\n\n4\n" // Approve warnings, Enter chooses another license, then skip.
				case "correct-and-replace":
					replacement := filepath.Join(t.TempDir(), "replacement.dat")
					if err := os.WriteFile(replacement, []byte(runtimeValidConnextLicense), 0600); err != nil {
						t.Fatal(err)
					}
					answers = "2\n" + source + ".missing\n" + source + "\n3\n2\n" + replacement + "\n\n"
				}
				var selectFunc prompt.SelectFunc
				var inputFunc func(string) (string, error)
				if command == "gateway" {
					app := gateway.NewGatewayApp(t.TempDir(), &out)
					app.In = strings.NewReader(answers)
					selectFunc, inputFunc = app.SelectFunc, app.InputFunc
				} else {
					app := spy.NewApp(t.TempDir(), &out)
					app.In = strings.NewReader(answers)
					selectFunc, inputFunc = app.SelectFunc, app.InputFunc
				}
				err := runtime.ensureConnextLicense(install, selectFunc, inputFunc)
				if (err == nil) != success {
					t.Fatalf("success=%v err=%v\n%s", success, err, out.String())
				}
				for _, path := range []string{saved, internalconnext.LicenseFilePath(install)} {
					data, err := os.ReadFile(path)
					if success {
						if err != nil || string(data) != runtimeValidConnextLicense {
							t.Fatalf("approved license missing at %s: %v", path, err)
						}
					} else if !os.IsNotExist(err) {
						t.Fatalf("unapproved license saved at %s: %v", path, err)
					}
				}
				if success {
					if err := runtime.ensureConnextLicense(install, nil, nil); err != nil {
						t.Fatalf("unchanged license prompted again: %v", err)
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
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				environmentPath, err = filepath.Rel(cwd, source)
				if err != nil {
					t.Fatal(err)
				}
			} else if scenario == "quoted-file" {
				environmentPath = "\"" + source + "\""
			}
			t.Setenv("RTI_LICENSE_FILE", environmentPath)
			selected := install
			if scenario == "external-install" {
				selected = internalconnext.Install{Path: t.TempDir()}
			}
			prompts := 0
			selectFunc := func(message string, choices []string, _ string) (string, error) {
				prompts++
				if choices[0] != internalconnext.DeclineLicensePrecheckLabel || !strings.Contains(message, "RTI_LICENSE_FILE="+source) {
					t.Fatalf("expected environment precheck, never copy or license selection: %s %#v", message, choices)
				}
				if scenario == "override-incomplete" {
					return internalconnext.ProceedLicensePrecheckLabel, nil
				}
				return internalconnext.DeclineLicensePrecheckLabel, nil
			}
			if scenario == "noninteractive-incomplete" {
				selectFunc = nil
			}
			for attempt := 0; attempt < 2; attempt++ {
				err := runtime.ensureConnextLicense(selected, selectFunc, nil)
				wantSuccess := scenario == "complete" || scenario == "external-install" || scenario == "override-incomplete" || scenario == "relative-file" || scenario == "quoted-file" || scenario == "symlink-file"
				if (err == nil) != wantSuccess {
					t.Fatalf("unexpected environment precheck: %v", err)
				}
				if err != nil && !strings.Contains(err.Error(), "Unset RTI_LICENSE_FILE") {
					t.Fatalf("missing override guidance: %v", err)
				}
			}
			wantPrompts := 0
			if scenario == "decline-incomplete" || scenario == "override-incomplete" {
				wantPrompts = 2
			}
			if prompts != wantPrompts || !strings.Contains(out.String(), "found via RTI_LICENSE_FILE") || !strings.Contains(out.String(), "Unset RTI_LICENSE_FILE") {
				t.Fatalf("missing repeated precheck or source guidance: prompts=%d output=%s", prompts, out.String())
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

func TestEnsureManagedConnextLicenseDownload(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "complete", false: "decline-incomplete"}[complete], func(t *testing.T) {
			runtime, install, shared := managedLicenseRuntime(t)
			contents := runtimeValidConnextLicense
			if !complete {
				contents = "FEATURE RTIPRO RTI 4. permanent SECRET_LICENSE"
			}
			api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, contents)}}
			runtime.License = commands.New(api, io.Discard)
			err := runtime.ensureConnextLicense(install, func(_ string, choices []string, _ string) (string, error) {
				if choices[0] == downloadConnextLicenseLabel {
					return downloadConnextLicenseLabel, nil
				}
				return internalconnext.DeclineLicensePrecheckLabel, nil
			}, nil)
			if (err == nil) != complete || api.calls != 1 {
				t.Fatalf("download result: err=%v calls=%d", err, api.calls)
			}
			for _, path := range []string{shared, internalconnext.LicenseFilePath(install)} {
				data, err := os.ReadFile(path)
				if complete {
					if err != nil || string(data) != contents {
						t.Fatalf("download not saved at %s: %v", path, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("declined license saved at %s: %v", path, err)
				}
			}
		})
	}
}

func TestEnsureConnextLicenseAcceptsEnteredPath(t *testing.T) {
	for _, scenario := range []string{"complete", "decline-import", "decline-features", "override-features", "missing-file"} {
		t.Run(scenario, func(t *testing.T) {
			runtime, install, shared := managedLicenseRuntime(t)
			home, _ := internalconnext.UserHomeDir()
			source := filepath.Join(home, "chosen license.dat")
			contents := runtimeValidConnextLicense
			if strings.HasSuffix(scenario, "features") {
				contents = "FEATURE RTIPRO RTI 4. permanent SECRET_LICENSE"
			}
			if err := os.WriteFile(source, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			menus, inputs := 0, 0
			var steps []string
			selectFunc := func(message string, choices []string, _ string) (string, error) {
				if len(choices) == 4 {
					menus++
					if choices[0] != downloadConnextLicenseLabel || choices[1] != enterConnextLicensePathLabel {
						t.Fatalf("path option must follow download: %#v", choices)
					}
					if menus > 1 {
						return cancelConnextLicenseLabel, nil
					}
					return enterConnextLicensePathLabel, nil
				}
				if choices[0] == internalconnext.ImportManagedLicenseLabel || choices[1] == internalconnext.ImportManagedLicenseLabel {
					steps = append(steps, "copy")
					if !strings.Contains(message, source) || !strings.Contains(message, shared) {
						t.Fatalf("import paths missing: %s", message)
					}
					if strings.HasSuffix(scenario, "features") {
						if choices[0] != internalconnext.DeclineManagedLicenseLabel || !strings.Contains(message, "License feature warnings were accepted.") {
							t.Fatalf("warning override must not default to copying: %s %#v", message, choices)
						}
					} else if choices[0] != internalconnext.ImportManagedLicenseLabel || !strings.Contains(message, "License feature check passed:") {
						t.Fatalf("valid import must show check and default to copying: %s %#v", message, choices)
					}
					if scenario == "decline-import" {
						return internalconnext.DeclineManagedLicenseLabel, nil
					}
					return internalconnext.ImportManagedLicenseLabel, nil
				}
				steps = append(steps, "features")
				if choices[0] != internalconnext.DeclineLicensePrecheckLabel || strings.Contains(message, "SECRET_LICENSE") {
					t.Fatalf("unsafe feature confirmation: %s %#v", message, choices)
				}
				if scenario == "override-features" {
					return internalconnext.ProceedLicensePrecheckLabel, nil
				}
				return internalconnext.DeclineLicensePrecheckLabel, nil
			}
			err := runtime.ensureConnextLicense(install, selectFunc, func(string) (string, error) {
				inputs++
				if scenario == "missing-file" && inputs == 1 {
					return "~/missing.dat", nil
				}
				return "~/chosen license.dat", nil
			})
			success := scenario == "complete" || scenario == "override-features" || scenario == "missing-file"
			if success != (err == nil) {
				t.Fatalf("unexpected import result: %v", err)
			}
			wantSteps := map[string]string{"complete": "copy", "decline-import": "copy", "decline-features": "features", "override-features": "features,copy", "missing-file": "copy"}[scenario]
			if got := strings.Join(steps, ","); got != wantSteps {
				t.Fatalf("prompt order = %q, want %q", got, wantSteps)
			}
			if scenario == "missing-file" && (inputs != 2 || menus != 1) {
				t.Fatal("mistyped path must allow correction without restarting setup")
			}
			for _, path := range []string{shared, internalconnext.LicenseFilePath(install)} {
				data, err := os.ReadFile(path)
				if success {
					if err != nil || string(data) != contents {
						t.Fatalf("import not saved at %s: %v", path, err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("unapproved import saved at %s: %v", path, err)
				}
			}
			if success {
				if err := runtime.ensureConnextLicense(install, nil, nil); err != nil {
					t.Fatalf("unchanged license required another prompt: %v", err)
				}
			}
		})
	}
}

func TestEnsureConnextLicenseCanChooseAnotherAfterDiscoveredFeatureWarnings(t *testing.T) {
	for _, next := range []string{downloadConnextLicenseLabel, enterConnextLicensePathLabel} {
		t.Run(next, func(t *testing.T) {
			runtime, install, shared := managedLicenseRuntime(t)
			if err := os.WriteFile(internalconnext.LicenseFilePath(install), []byte("FEATURE RTIPRO RTI 4. 01-jan-2000 SECRET_LICENSE"), 0600); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(t.TempDir(), "replacement.dat")
			if err := os.WriteFile(source, []byte(runtimeValidConnextLicense), 0600); err != nil {
				t.Fatal(err)
			}
			api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, runtimeValidConnextLicense)}}
			runtime.License = commands.New(api, io.Discard)
			var steps []string
			err := runtime.ensureConnextLicense(install, func(_ string, choices []string, _ string) (string, error) {
				if _, err := os.Lstat(shared); !os.IsNotExist(err) {
					t.Fatal("license was copied before approval")
				}
				switch len(choices) {
				case 3:
					steps = append(steps, "features")
					if choices[0] != internalconnext.DeclineLicensePrecheckLabel || choices[2] != internalconnext.DeclineManagedLicenseLabel {
						t.Fatalf("missing default No or recovery option: %#v", choices)
					}
					return internalconnext.DeclineManagedLicenseLabel, nil
				case 4:
					steps = append(steps, "menu")
					return next, nil
				default:
					steps = append(steps, "copy")
					return internalconnext.ImportManagedLicenseLabel, nil
				}
			}, func(string) (string, error) { return source, nil })
			if err != nil {
				t.Fatal(err)
			}
			want := "features,menu"
			if next == enterConnextLicensePathLabel {
				want += ",copy"
			}
			if strings.Join(steps, ",") != want {
				t.Fatalf("unexpected recovery flow: %v", steps)
			}
			for _, path := range []string{shared, internalconnext.LicenseFilePath(install)} {
				if data, err := os.ReadFile(path); err != nil || string(data) != runtimeValidConnextLicense {
					t.Fatalf("replacement license not saved: %v", err)
				}
			}
		})
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
	api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, runtimeValidConnextLicense)}}
	var out bytes.Buffer
	runtime := &Runtime{Out: &out, License: commands.New(api, &out)}
	licensePath := filepath.Join(install, "rti_license.dat")
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string, _ string) (string, error) {
		if !strings.Contains(message, install) || len(choices) != 4 || choices[1] != enterConnextLicensePathLabel || choices[2] != manualDownloadConnextLicenseLabel {
			t.Fatalf("unexpected prompt: %q %#v", message, choices)
		}
		return manualDownloadConnextLicenseLabel, nil
	}, nil)
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
		"Copy the license file to " + licensePath,
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
	api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(http.StatusOK, runtimeValidConnextLicense)}}
	runtime := &Runtime{License: commands.New(api, io.Discard)}
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string, _ string) (string, error) {
		if !strings.Contains(message, install) || len(choices) != 4 || choices[3] != cancelConnextLicenseLabel {
			t.Fatalf("unexpected prompt: %q %#v", message, choices)
		}
		return cancelConnextLicenseLabel, nil
	}, nil)
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
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, nil, nil)
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
	err := runtime.ensureConnextLicense(internalconnext.Install{Path: install, Version: "7.7.0"}, func(message string, choices []string, _ string) (string, error) {
		return downloadConnextLicenseLabel, nil
	}, nil)
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
		result, err := resolveRuntimeConnext(values, true, skip, internalconnext.ConfirmationFromSelector(func(string, []string, string) (string, error) {
			t.Fatal("unexpected selector")
			return "", nil
		}), internalconnext.DiscoveryOptions{})
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
	values := map[string]any{"runtime": map[string]any{"connext_home": directory}}
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
