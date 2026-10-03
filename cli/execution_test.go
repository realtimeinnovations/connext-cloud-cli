// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/cloudapi"
	"github.com/realtimeinnovations/connext-cloud-cli/commands"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/update"
)

func TestExecuteJSONRestoresRuntimeAndSkipsAutomaticUpdate(t *testing.T) {
	var out bytes.Buffer
	client := cloudapi.New(func() (string, error) { return "https://example.test", nil }, func() (map[string]string, error) { return nil, nil })
	client.Out = &out
	client.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		return stringResponse(http.StatusOK, `{"databuses":{}}`), nil
	})
	runner := commands.New(client, &out)
	manager := config.New(filepath.Join(t.TempDir(), "config.json"))
	authManager := auth.New(parserAuthConfigProvider{}, filepath.Join(t.TempDir(), "credentials.json"))
	updater := update.New(manager, &out)
	updater.CurrentVersion = func() string { return "1.2.3" }
	updater.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected automatic update request")
		return stringResponse(http.StatusOK, `{"tag_name":"v1.2.4"}`), nil
	})
	runtime := &app.Runtime{Commands: runner, CloudAPI: client, Config: manager, Auth: authManager, Updater: updater}
	if err := Execute([]string{"databus", "list", "--format", "json"}, &out, io.Discard, runtime); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"schema_version":"1"`) {
		t.Fatalf("unexpected JSON result: %s", out.String())
	}
	if runtime.Commands != runner || runner.JSON || client.Out != &out || manager.NonInteractive || authManager.NonInteractive {
		t.Fatal("JSON invocation left runtime settings changed")
	}
	out.Reset()
	if err := Execute([]string{"databus", "list", "--non-interactive"}, &out, io.Discard, runtime); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "schema_version") {
		t.Fatalf("JSON output leaked into text invocation: %s", out.String())
	}
}

func TestExecutionErrorPreservesWrappedHTTPFailure(t *testing.T) {
	cause := httputil.NewStatusError(http.StatusForbidden, []byte(`{"message":"denied"}`))
	err := executionFailure(fmt.Errorf("query failed: %w", cause), true, true)
	var typed *clierror.Error
	var status *httputil.StatusError
	if !errors.As(err, &typed) || typed.Code != "PERMISSION_DENIED" || !errors.As(err, &status) || status != cause {
		t.Fatalf("typed cause was lost: %v", err)
	}
	var stderr bytes.Buffer
	if exit := ReportError(err, &stderr); exit != 3 {
		t.Fatalf("exit=%d, want 3", exit)
	}
	var result struct {
		Error *clierror.Error `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &result); err != nil || result.Error == nil || result.Error.HTTPStatus != 403 {
		t.Fatalf("unexpected structured error: %s (%v)", stderr.String(), err)
	}
}

func TestLicenseJSONUsesAndRestoresEvaluationRunner(t *testing.T) {
	var stdout, diagnostics bytes.Buffer
	client := cloudapi.New(func() (string, error) { return "https://evaluation.example.test", nil }, func() (map[string]string, error) { return nil, nil })
	client.Out = &diagnostics
	runtime := &app.Runtime{License: commands.New(client, &diagnostics), WorkAuth: auth.New(parserAuthConfigProvider{}, filepath.Join(t.TempDir(), "evaluation.json"))}
	original := runtime.License
	calls := 0
	client.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		calls++
		if !runtime.WorkAuth.NonInteractive || !runtime.License.JSON {
			t.Error("license JSON did not disable interaction")
		}
		// Retry/proxy diagnostics use Client.Out; they must not corrupt JSON.
		_, _ = fmt.Fprintln(client.Out, "transport diagnostic")
		return stringResponse(http.StatusOK, "LICENSE DATA"), nil
	})
	target := filepath.Join(t.TempDir(), "license.dat")
	if err := Execute([]string{"license", "get", "--output", target, "--format", "json"}, &stdout, io.Discard, runtime); err != nil {
		t.Fatal(err)
	}
	var result struct {
		SchemaVersion string `json:"schema_version"`
		Data          struct {
			Path string `json:"artifact_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.SchemaVersion != "1" || result.Data.Path != target || diagnostics.Len() != 0 || calls != 1 {
		t.Fatalf("result=%q diagnostic=%q error=%v calls=%d", stdout.String(), diagnostics.String(), err, calls)
	}
	if runtime.License != original || original.JSON || client.Out != &diagnostics || runtime.WorkAuth.NonInteractive {
		t.Fatal("license JSON settings were not restored")
	}

	stdout.Reset()
	client.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		return stringResponse(http.StatusForbidden, `{"message":"denied"}`), nil
	})
	err := Execute([]string{"license", "get", "--output", target, "--format", "json"}, &stdout, io.Discard, runtime)
	var stderr bytes.Buffer
	if err == nil || ReportError(err, &stderr) != 3 || stdout.Len() != 0 || !json.Valid(stderr.Bytes()) {
		t.Fatalf("license failure streams: %q %q (%v)", stdout.String(), stderr.String(), err)
	}
	if runtime.License != original || original.JSON || client.Out != &diagnostics || runtime.WorkAuth.NonInteractive {
		t.Fatal("failed license JSON settings were not restored")
	}
}

func TestGreenCommandArgumentFailuresAreStructured(t *testing.T) {
	for _, args := range [][]string{
		{"network", "delete"}, {"observability", "create"},
		{"observability", "list", "--short"},
		{"databus", "disable"}, {"databus", "set-observability", "--name", "demo"},
		{"edge-provisioning", "service", "query"},
		{"edge-provisioning", "domain-template", "create", "--service", "demo", "--custom-governance-file", "input.xml"},
		{"network", "list", "--format", "yaml"},
	} {
		args = append(args, "--format", "json")
		// Keep the invalid format in its dedicated case (last value wins).
		if args[0] == "network" && args[1] == "list" {
			args = []string{"network", "list", "--format", "yaml"}
		}
		var out, stderr bytes.Buffer
		err := Execute(args, &out, &stderr, nil)
		if err == nil || ReportError(err, &stderr) != 2 || out.Len() != 0 {
			t.Fatalf("%v: stdout=%q stderr=%q error=%v", args, out.String(), stderr.String(), err)
		}
		if args[len(args)-1] == "json" && !json.Valid(stderr.Bytes()) {
			t.Fatalf("not structured: %q", stderr.String())
		}
	}
}
