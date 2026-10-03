// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

func TestDatabusWaitsForConfirmedTerminalState(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		t.Run(map[bool]string{false: "creation", true: "deletion"}[deleting], func(t *testing.T) {
			polls, waits := 0, 0
			pending := "creating"
			if deleting {
				pending = "deleting"
			}
			api := &fakeAPI{getFunc: func(string) (*http.Response, error) {
				polls++
				if polls < 3 {
					return newJSONResponse(http.StatusOK, map[string]any{"status": pending}), nil
				}
				if deleting {
					return newTextResponse(http.StatusNotFound, "missing"), nil
				}
				return newJSONResponse(http.StatusOK, map[string]any{"status": "active"}), nil
			}}
			runner := New(api, io.Discard)
			runner.Sleep = func(time.Duration) { waits++ }
			resource, err := runner.waitForDatabusTerminal("demo", deleting)
			if err != nil || polls != 3 || waits != 2 || resource["name"] != "demo" {
				t.Fatalf("resource=%v error=%v polls=%d waits=%d", resource, err, polls, waits)
			}
		})
	}
}

func TestDatabusPollingPreservesFailure(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		api := &fakeAPI{getFunc: func(string) (*http.Response, error) {
			return newTextResponse(status, "request rejected"), nil
		}}
		runner := New(api, io.Discard)
		_, err := runner.waitForDatabusTerminal("demo", true)
		var statusErr *httputil.StatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode != status {
			t.Fatalf("status %d lost polling error: %v", status, err)
		}
	}
}

func TestDatabusWaitTimeoutIsTyped(t *testing.T) {
	api := &fakeAPI{getFunc: func(string) (*http.Response, error) {
		return newJSONResponse(http.StatusOK, map[string]any{"status": "deleting"}), nil
	}}
	runner := New(api, io.Discard)
	runner.Sleep = func(time.Duration) {}
	_, err := runner.waitForDatabusTerminal("demo", true)
	var typed *clierror.Error
	if !errors.As(err, &typed) || typed.Code != "TIMEOUT" || typed.ExitCode() != 6 {
		t.Fatalf("expected typed timeout, got %v", err)
	}
}

type failingDatabusWriter struct{}

func (failingDatabusWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestDatabusOutputFailureIsNotSuccess(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /databuses?extra_fields=true": newJSONResponse(http.StatusOK, map[string]any{"databuses": map[string]any{}}),
	}}
	runner := New(api, failingDatabusWriter{})
	runner.JSON = true
	if err := runner.ListDatabuses(false); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expected output failure, got %v", err)
	}
}

func TestDatabusJSONFailureDoesNotWriteSuccess(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /databuses": newJSONResponse(http.StatusCreated, map[string]any{}),
	}, getFunc: func(string) (*http.Response, error) {
		return newJSONResponse(http.StatusOK, map[string]any{"status": "error"}), nil
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.JSON = true
	if err := runner.CreateDatabus("demo", 2, "", "", true); err == nil || out.Len() != 0 {
		t.Fatalf("error=%v stdout=%q", err, out.String())
	}
}

func TestDatabusResultUsesEscapedName(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /databuses/demo%2Fone": newJSONResponse(http.StatusOK, map[string]any{"status": "active"}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.JSON = true
	if err := runner.QueryDatabus("demo/one"); err != nil {
		t.Fatal(err)
	}
	var result resultEnvelope
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.SchemaVersion != "1" {
		t.Fatalf("unexpected result: %s (%v)", out.String(), err)
	}
}

// Every green command must propagate rejected requests, regardless of output mode.
func greenOperations() []struct {
	name, request string
	run           func(*Runner) error
} {
	return []struct {
		name, request string
		run           func(*Runner) error
	}{
		{"databus disable", "PATCH /databuses/demo", func(r *Runner) error { return r.UpdateDatabusStatus("demo", "disable") }},
		{"databus resume", "PATCH /databuses/demo", func(r *Runner) error { return r.UpdateDatabusStatus("demo", "resume") }},
		{"databus link", "PATCH /databuses/demo", func(r *Runner) error { return r.UpdateObservabilityLink("demo", "obs") }},
		{"databus unlink", "PATCH /databuses/demo", func(r *Runner) error { return r.UpdateObservabilityLink("demo", nil) }},
		{"databus filters", "PATCH /databuses/demo", func(r *Runner) error { return r.UpdateFilters("demo", "filters.json") }},
		{"databus add user", "POST /databuses/demo/users/user@example.com/", func(r *Runner) error { return r.AddUserToDatabus("demo", "user@example.com") }},
		{"databus remove user", "DELETE /databuses/demo/users/user@example.com/", func(r *Runner) error { return r.RemoveUserFromDatabus("demo", "user@example.com") }},
		{"app create", "POST /databuses/demo/applications", func(r *Runner) error { return r.CreateApplication("demo", "subscriber", 7777, "app", "", false) }},
		{"app delete", "DELETE /databuses/demo/applications/subscriber", func(r *Runner) error { return r.DeleteApplication("demo", "subscriber") }},
		{"app client list", "GET /databuses/demo/applications/subscriber/clients", func(r *Runner) error { return r.ListAppClients("demo", "subscriber") }},
		{"app client revoke", "DELETE /databuses/demo/applications/subscriber/clients/device-1", func(r *Runner) error { return r.RevokeAppClient("demo", "subscriber", "device-1") }},
		{"network list", "GET /networks", func(r *Runner) error { return r.ListNetworks() }},
		{"network delete", "DELETE /networks/demo", func(r *Runner) error { return r.DeleteNetwork("demo") }},
		{"obs create", "POST /databuses", func(r *Runner) error { return r.CreateObsService("demo", "", true) }},
		{"obs list", "GET /databuses?extra_fields=true", func(r *Runner) error { return r.ListObservabilityServices(false) }},
		{"obs query", "GET /databuses/demo", func(r *Runner) error { return r.QueryObservabilityService("demo") }},
		{"obs delete", "DELETE /databuses/demo", func(r *Runner) error { return r.DeleteObservabilityService("demo") }},
		{"obs disable", "PATCH /databuses/demo", func(r *Runner) error { return r.UpdateDatabusStatus("demo", "disable") }},
		{"obs resume", "PATCH /databuses/demo", func(r *Runner) error { return r.UpdateDatabusStatus("demo", "resume") }},
		{"license get", "POST /licenses", func(r *Runner) error { return r.GetLicense(nil, "") }},
		{"service create", "POST /edge-systems", func(r *Runner) error { return r.CreateEdgeSystem("demo", "") }},
		{"service list", "GET /edge-systems", func(r *Runner) error { return r.ListEdgeSystems() }},
		{"service query", "GET /edge-systems/demo", func(r *Runner) error { return r.QueryEdgeSystem("demo") }},
		{"service delete", "DELETE /edge-systems/demo", func(r *Runner) error { return r.DeleteEdgeSystem("demo") }},
		{"governance create", "POST /edge-systems/demo/governance-templates", func(r *Runner) error { return r.CreateGovernanceTemplate("demo", "tpl", "governance.xml") }},
		{"governance list", "GET /edge-systems/demo/governance-templates", func(r *Runner) error { return r.ListGovernanceTemplates("demo") }},
		{"governance delete", "DELETE /edge-systems/demo/governance-templates/tpl", func(r *Runner) error { return r.DeleteGovernanceTemplate("demo", "tpl") }},
		{"permissions create", "POST /edge-systems/demo/permissions-templates", func(r *Runner) error { return r.CreatePermissionsTemplate("demo", "tpl", "permissions.xml") }},
		{"permissions list", "GET /edge-systems/demo/permissions-templates", func(r *Runner) error { return r.ListPermissionsTemplates("demo") }},
		{"permissions get", "GET /edge-systems/demo/permissions-templates/tpl", func(r *Runner) error { return r.GetPermissionsTemplate("demo", "tpl") }},
		{"permissions delete", "DELETE /edge-systems/demo/permissions-templates/tpl", func(r *Runner) error { return r.DeletePermissionsTemplate("demo", "tpl") }},
		{"domain create", "POST /edge-systems/demo/domain-templates", func(r *Runner) error { return r.CreateDomainTemplate("demo", 0, "gov", "", "", "") }},
		{"domain list", "GET /edge-systems/demo/domain-templates", func(r *Runner) error { return r.ListDomainTemplates("demo") }},
		{"domain delete", "DELETE /edge-systems/demo/domain-templates/tpl", func(r *Runner) error { return r.DeleteDomainTemplate("demo", "tpl") }},
		{"participant template create", "POST /edge-systems/demo/participant-templates", func(r *Runner) error { return r.CreateParticipantTemplate("demo", "tpl", "perms", 60) }},
		{"participant template list", "GET /edge-systems/demo/participant-templates", func(r *Runner) error { return r.ListParticipantTemplates("demo") }},
		{"participant template get", "GET /edge-systems/demo/participant-templates/tpl", func(r *Runner) error { return r.GetParticipantTemplate("demo", "tpl") }},
		{"participant template delete", "DELETE /edge-systems/demo/participant-templates/tpl", func(r *Runner) error { return r.DeleteParticipantTemplate("demo", "tpl") }},
		{"campaign create", "POST /edge-systems/demo/campaigns", func(r *Runner) error { return r.CreateCampaign("demo", "tpl", "devices.json", "domain") }},
		{"campaign list", "GET /edge-systems/demo/campaigns", func(r *Runner) error { return r.ListCampaigns("demo") }},
		{"campaign list devices", "GET /edge-systems/demo/campaigns/camp/devices", func(r *Runner) error { return r.ListCampaignDevices("demo", "camp") }},
		{"campaign delete", "DELETE /edge-systems/demo/campaigns/camp", func(r *Runner) error { return r.DeleteCampaign("demo", "camp") }},
		{"participant list", "GET /edge-systems/demo/devices", func(r *Runner) error { return r.ListEdgeDevices("demo") }},
		{"participant revoke", "DELETE /edge-systems/demo/participants/tpl/campaigns/camp/devices/SN1", func(r *Runner) error { return r.RevokeDevice("demo", "tpl", "camp", "SN1") }},
	}
}

func greenInputFile(path string) ([]byte, error) {
	if path == "devices.json" {
		return []byte(`[{"serial":"SN1","macs":[]}]`), nil
	}
	if path == "filters.json" {
		return []byte(`[{"topic_name":"Square","topic_filter":"x > 1"}]`), nil
	}
	return []byte(`<dds/>`), nil
}

func TestGreenOperationsPreserveHTTPFailures(t *testing.T) {
	for _, tc := range greenOperations() {
		for _, jsonMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", tc.name, jsonMode), func(t *testing.T) {
				api := &fakeAPI{responses: map[string]*http.Response{tc.request: newTextResponse(http.StatusForbidden, `{"message":"denied"}`)}}
				var out bytes.Buffer
				runner := New(api, &out)
				runner.JSON, runner.ReadFile = jsonMode, greenInputFile
				err := tc.run(runner)
				var status *httputil.StatusError
				if !errors.As(err, &status) || status.StatusCode != 403 || clierror.From(err).ExitCode() != 3 {
					t.Fatalf("HTTP failure swallowed or changed: %v", err)
				}
				if jsonMode && out.Len() != 0 {
					t.Fatalf("failure wrote stdout: %q", out.String())
				}
			})
		}
	}
}

func TestGreenOperationsEmitOneJSONResult(t *testing.T) {
	for _, tc := range greenOperations() {
		t.Run(tc.name, func(t *testing.T) {
			status := http.StatusOK
			if strings.HasPrefix(tc.request, "POST ") {
				status = http.StatusCreated
			}
			if tc.name == "service create" {
				status = http.StatusAccepted
			}
			if tc.name == "license get" {
				status = http.StatusOK
			}
			body := map[string]any{"clients": map[string]any{"device-1": map[string]any{"id": "device-1"}}, "id": "backend-id", "status": "active", "databuses": map[string]any{"demo": map[string]any{"kind": "telemetry"}}}
			responses := map[string]*http.Response{tc.request: newJSONResponse(status, body)}
			if tc.name == "obs create" {
				responses["GET /databuses/demo"] = newJSONResponse(http.StatusOK, map[string]any{"status": "active"})
			}
			if tc.name == "obs delete" {
				responses["GET /databuses/demo"] = newTextResponse(http.StatusNotFound, "missing")
			}
			var out bytes.Buffer
			runner := New(&fakeAPI{responses: responses}, &out)
			runner.JSON, runner.ReadFile = true, greenInputFile
			if err := tc.run(runner); err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				SchemaVersion string         `json:"schema_version"`
				Data          map[string]any `json:"data"`
			}
			if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || envelope.SchemaVersion != "1" || envelope.Data == nil {
				t.Fatalf("not one versioned result: %q (%v)", out.String(), err)
			}
			switch tc.name {
			case "app create", "app delete", "app client list", "app client revoke":
				if envelope.Data["databus"] != "demo" || envelope.Data["application"] != "subscriber" {
					t.Fatalf("missing application target: %s", out.String())
				}
				if tc.name == "app create" && envelope.Data["resource"].(map[string]any)["id"] != "backend-id" {
					t.Fatalf("lost backend ID: %s", out.String())
				}
				if tc.name == "app delete" && envelope.Data["status"] != "deleted" {
					t.Fatalf("missing deletion confirmation: %s", out.String())
				}
				if tc.name == "app client revoke" && (envelope.Data["client_id"] != "device-1" || envelope.Data["status"] != "revoked") {
					t.Fatalf("missing revocation confirmation: %s", out.String())
				}
			}
			if tc.name == "service create" && envelope.Data["status"] != "accepted" {
				t.Fatalf("accepted creation claimed completion: %s", out.String())
			}
			if tc.name == "databus filters" && strings.Contains(out.String(), "Converting JSON") {
				t.Fatal("conversion diagnostic leaked into JSON")
			}
		})
	}
}

func TestGreenFileValidationFailsBeforeAPI(t *testing.T) {
	operations := []func(*Runner) error{
		func(r *Runner) error { return r.CreateApplication("demo", "subscriber", 7777, "", "input", false) },
		func(r *Runner) error { return r.UpdateFilters("demo", "input") },
		func(r *Runner) error { return r.CreateGovernanceTemplate("demo", "tpl", "input") },
		func(r *Runner) error { return r.CreatePermissionsTemplate("demo", "tpl", "input") },
		func(r *Runner) error { return r.CreateDomainTemplate("demo", 0, "", "", "input", "gov") },
		func(r *Runner) error { return r.CreateCampaign("demo", "tpl", "input", "domain") },
	}
	for i, run := range operations {
		api := &fakeAPI{}
		var out bytes.Buffer
		runner := New(api, &out)
		runner.JSON = true
		runner.ReadFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
		err := run(runner)
		if clierror.From(err).ExitCode() != 2 || !errors.Is(err, os.ErrNotExist) || out.Len() != 0 || api.lastPath != "" {
			t.Fatalf("operation %d: error=%v stdout=%q API=%q", i, err, out.String(), api.lastPath)
		}
	}
	for _, body := range []string{`{broken`, `null`} {
		for _, campaign := range []bool{false, true} {
			api := &fakeAPI{}
			runner := New(api, io.Discard)
			runner.ReadFile = func(string) ([]byte, error) { return []byte(body), nil }
			var err error
			if campaign {
				err = runner.CreateCampaign("demo", "tpl", "input.json", "domain")
			} else {
				err = runner.UpdateFilters("demo", "input.json")
			}
			if err == nil || clierror.From(err).ExitCode() != 2 || api.lastPath != "" {
				t.Fatalf("invalid input %q: %v", body, err)
			}
		}
	}
}

func TestObservabilityRejectsUnexpectedTerminalStates(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		for _, pollStatus := range []int{200, 401, 503} {
			api := &fakeAPI{responses: map[string]*http.Response{
				"POST /databuses":        newTextResponse(http.StatusCreated, `{}`),
				"DELETE /databuses/demo": newTextResponse(http.StatusNoContent, ""),
			}, getFunc: func(string) (*http.Response, error) {
				return newJSONResponse(pollStatus, map[string]any{"status": "error"}), nil
			}}
			var out bytes.Buffer
			runner := New(api, &out)
			runner.JSON = true
			var err error
			if deleting {
				err = runner.DeleteObservabilityService("demo")
			} else {
				err = runner.CreateObsService("demo", "", true)
			}
			if err == nil || out.Len() != 0 {
				t.Fatalf("deleting=%t status=%d error=%v stdout=%q", deleting, pollStatus, err, out.String())
			}
			expected := "OPERATION_FAILED"
			if pollStatus == 401 {
				expected = "AUTH_REQUIRED"
			}
			if pollStatus == 503 {
				expected = "API_ERROR"
			}
			if clierror.From(err).Code != expected {
				t.Fatalf("wrong polling error: %v", err)
			}
		}
	}
}

func TestLicenseJSONReportsSavedArtifact(t *testing.T) {
	target := filepath.Join(t.TempDir(), "rti_license.dat")
	api := &fakeAPI{responses: map[string]*http.Response{"POST /licenses": newTextResponse(http.StatusOK, "LICENSE DATA")}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.JSON = true
	if err := runner.GetLicense(nil, target); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "LICENSE DATA" {
		t.Fatalf("license not saved: %s (%v)", body, err)
	}
	var result struct {
		Data struct {
			ArtifactPath string `json:"artifact_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Data.ArtifactPath != target {
		t.Fatalf("missing artifact path: %s (%v)", out.String(), err)
	}
	negative := -1
	out.Reset()
	if err := runner.GetLicense(&negative, target); err == nil || clierror.From(err).ExitCode() != 2 || out.Len() != 0 {
		t.Fatalf("negative expiration succeeded: %v", err)
	}
}
