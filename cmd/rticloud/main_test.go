// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/rtipaths"
)

// The subprocess runs the production entry point, including os.Exit and stream
// handling. Only home-directory discovery is redirected to an isolated fixture.
func TestCLIProcess(t *testing.T) {
	if os.Getenv("RTICLOUD_CLI_TEST_CHILD") == "1" {
		home := os.Getenv("RTICLOUD_CLI_TEST_HOME")
		rtipaths.UserHomeDir = func() (string, error) { return home, nil }
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
				break
			}
		}
		main()
		os.Exit(0)
	}

	for _, tc := range []struct {
		name          string
		args          []string
		status        int
		mutation      int
		mutationBody  string
		textWant      string
		resource      string
		list          string
		noCredentials bool
		expired       bool
		apiKey        bool
		noConfig      bool
		text          bool
		jsonOnly      bool
		code          string
		exit          int
		calls         int32
	}{
		{name: "list", args: []string{"databus", "list"}, calls: 1},
		{name: "query", args: []string{"databus", "query", "--name", "demo"}, calls: 1},
		{name: "create active", args: []string{"databus", "create", "--name", "demo"}, calls: 2},
		{name: "delete confirmed", args: []string{"databus", "delete", "--name", "demo"}, status: 404, calls: 2},
		{name: "disable", args: []string{"databus", "disable", "--name", "demo"}, calls: 1},
		{name: "resume", args: []string{"databus", "resume", "--name", "demo"}, calls: 1},
		{name: "link obs", args: []string{"databus", "set-observability", "--name", "demo", "--service", "obs"}, calls: 1},
		{name: "unlink obs", args: []string{"databus", "set-observability", "--name", "demo", "--unlink"}, calls: 1},
		{name: "add user", args: []string{"databus", "add-user", "--name", "demo", "--email", "user@example.com"}, calls: 1},
		{name: "remove user", args: []string{"databus", "remove-user", "--name", "demo", "--email", "user@example.com"}, calls: 1},
		{name: "app create", args: []string{"databus", "app", "create", "--name", "demo", "--app-name", "subscriber"}, calls: 1},
		{name: "app delete", args: []string{"databus", "app", "delete", "--name", "demo", "--app-name", "subscriber"}, calls: 1},
		{name: "app client list", args: []string{"databus", "app", "client", "list", "--name", "demo", "--app-name", "subscriber"}, resource: `{"clients":{"device-1":{"id":"device-1"}}}`, calls: 1},
		{name: "app client revoke", args: []string{"databus", "app", "client", "revoke", "--name", "demo", "--app-name", "subscriber", "--client-id", "device-1"}, calls: 1},
		{name: "app create conflict", args: []string{"databus", "app", "create", "--name", "demo", "--app-name", "subscriber"}, mutation: 409, code: "CONFLICT", exit: 5, calls: 1},
		{name: "app delete denied", args: []string{"databus", "app", "delete", "--name", "demo", "--app-name", "subscriber"}, mutation: 403, code: "PERMISSION_DENIED", exit: 3, calls: 1},
		{name: "app client list denied", args: []string{"databus", "app", "client", "list", "--name", "demo", "--app-name", "subscriber"}, status: 403, code: "PERMISSION_DENIED", exit: 3, calls: 1},
		{name: "app client revoke missing", args: []string{"databus", "app", "client", "revoke", "--name", "demo", "--app-name", "subscriber", "--client-id", "device-1"}, mutation: 404, code: "NOT_FOUND", exit: 4, calls: 1},
		{name: "app create missing name", args: []string{"databus", "app", "create"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "app create invalid kind", args: []string{"databus", "app", "create", "--name", "demo", "--app-name", "subscriber", "--kind", "unknown"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "app create missing config", args: []string{"databus", "app", "create", "--name", "demo", "--app-name", "subscriber", "--config", "/no-such-rticloud-input/app.json"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "app delete missing app", args: []string{"databus", "app", "delete", "--name", "demo"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "app client list missing app", args: []string{"databus", "app", "client", "list", "--name", "demo"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "app client revoke missing ID", args: []string{"databus", "app", "client", "revoke", "--name", "demo", "--app-name", "subscriber"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "app create invalid response", args: []string{"databus", "app", "create", "--name", "demo", "--app-name", "subscriber"}, mutationBody: `{broken`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "app create null response", args: []string{"databus", "app", "create", "--name", "demo", "--app-name", "subscriber"}, mutationBody: `null`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "app client list missing collection", args: []string{"databus", "app", "client", "list", "--name", "demo", "--app-name", "subscriber"}, resource: `{}`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "app client list null collection", args: []string{"databus", "app", "client", "list", "--name", "demo", "--app-name", "subscriber"}, resource: `{"clients":null}`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "app client list invalid collection", args: []string{"databus", "app", "client", "list", "--name", "demo", "--app-name", "subscriber"}, resource: `{"clients":"bad"}`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "app JSON prevents login", args: []string{"databus", "app", "create", "--name", "demo", "--app-name", "subscriber"}, jsonOnly: true, noCredentials: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "app client JSON prevents login", args: []string{"databus", "app", "client", "list", "--name", "demo", "--app-name", "subscriber"}, jsonOnly: true, noCredentials: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "app create text", args: []string{"databus", "app", "create", "--name", "demo", "--app-name", "subscriber"}, text: true, textWant: `"message": "mutation response"`, calls: 1},
		{name: "app delete text", args: []string{"databus", "app", "delete", "--name", "demo", "--app-name", "subscriber"}, text: true, textWant: "Application 'subscriber' successfully deleted", calls: 1},
		{name: "app client list text", args: []string{"databus", "app", "client", "list", "--name", "demo", "--app-name", "subscriber"}, resource: `{"clients":{"device-1":{"id":"device-1"}}}`, text: true, textWant: `"device-1"`, calls: 1},
		{name: "app client revoke text", args: []string{"databus", "app", "client", "revoke", "--name", "demo", "--app-name", "subscriber", "--client-id", "device-1"}, text: true, textWant: "Client 'device-1' revoked successfully.", calls: 1},
		{name: "app delete text error", args: []string{"databus", "app", "delete", "--name", "demo", "--app-name", "subscriber"}, mutation: 404, text: true, exit: 4, calls: 1},
		{name: "client register JSON remains unsupported", args: []string{"databus", "app", "client", "register"}, code: "FORMAT_UNSUPPORTED", exit: 2},
		{name: "network list", args: []string{"network", "list"}, calls: 1},
		{name: "network delete", args: []string{"network", "delete", "--name", "demo"}, calls: 1},
		{name: "network forbidden", args: []string{"network", "list"}, status: 403, code: "PERMISSION_DENIED", exit: 3, calls: 1},
		{name: "network delete rejected", args: []string{"network", "delete", "--name", "demo"}, mutation: 403, code: "PERMISSION_DENIED", exit: 3, calls: 1},
		{name: "network invalid response", args: []string{"network", "list"}, resource: `{broken`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "obs create active", args: []string{"observability", "create", "--name", "demo"}, calls: 2},
		{name: "obs list", args: []string{"observability", "list"}, list: `{"databuses":{"obs":{"kind":"telemetry"},"demo":{"kind":"databus"}}}`, calls: 1},
		{name: "obs query", args: []string{"observability", "query", "--name", "demo"}, calls: 1},
		{name: "obs delete confirmed", args: []string{"observability", "delete", "--name", "demo"}, status: 404, calls: 2},
		{name: "obs disable", args: []string{"observability", "disable", "--name", "demo"}, calls: 1},
		{name: "obs resume", args: []string{"observability", "resume", "--name", "demo"}, calls: 1},
		{name: "obs create rejected", args: []string{"observability", "create", "--name", "demo"}, mutation: 409, code: "CONFLICT", exit: 5, calls: 1},
		{name: "obs create terminal error", args: []string{"observability", "create", "--name", "demo"}, resource: `{"status":"error"}`, code: "OPERATION_FAILED", exit: 1, calls: 2},
		{name: "obs delete remains active", args: []string{"observability", "delete", "--name", "demo"}, code: "OPERATION_FAILED", exit: 1, calls: 2},
		{name: "obs delete polling denied", args: []string{"observability", "delete", "--name", "demo"}, status: 401, code: "AUTH_REQUIRED", exit: 3, calls: 2},
		{name: "obs short JSON", args: []string{"observability", "list", "--short"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "service accepted", args: []string{"edge-provisioning", "service", "create", "--name", "demo"}, mutation: 202, calls: 1},
		{name: "service list", args: []string{"edge-provisioning", "service", "list"}, calls: 1},
		{name: "service query", args: []string{"edge-provisioning", "service", "query", "--name", "demo"}, calls: 1},
		{name: "service delete", args: []string{"edge-provisioning", "service", "delete", "--name", "demo"}, calls: 1},
		{name: "template list", args: []string{"edge-provisioning", "governance-template", "list", "--service", "demo"}, calls: 1},
		{name: "template rejected", args: []string{"edge-provisioning", "permissions-template", "get", "--service", "demo", "--name", "tpl"}, status: 404, code: "NOT_FOUND", exit: 4, calls: 1},
		{name: "campaign list", args: []string{"edge-provisioning", "campaign", "list", "--service", "demo"}, calls: 1},
		{name: "participant revoke", args: []string{"edge-provisioning", "participant", "revoke", "--service", "demo", "--participant-id", "tpl", "--campaign-id", "camp", "--serial", "SN1"}, calls: 1},
		{name: "network missing name", args: []string{"network", "delete"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "obs missing name", args: []string{"observability", "create"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "template missing service", args: []string{"edge-provisioning", "governance-template", "list"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "filter missing file", args: []string{"databus", "update-filters", "--name", "demo", "--filters", "/no-such-rticloud-input/filters.json"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "template missing file", args: []string{"edge-provisioning", "governance-template", "create", "--service", "demo", "--name", "tpl", "--governance-file", "/no-such-rticloud-input/governance.xml"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "license invalid expiration", args: []string{"license", "get", "--expiration-days", "-1"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "license missing credentials", args: []string{"license", "get"}, noCredentials: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "network JSON prevents login", args: []string{"network", "list"}, jsonOnly: true, noCredentials: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "obs JSON prevents login", args: []string{"observability", "list"}, jsonOnly: true, noCredentials: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "service JSON prevents login", args: []string{"edge-provisioning", "service", "list"}, jsonOnly: true, noCredentials: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "API key", args: []string{"databus", "list"}, noCredentials: true, apiKey: true, calls: 2},
		{name: "expired credentials with API key", args: []string{"databus", "list"}, expired: true, apiKey: true, calls: 2},
		{name: "unauthorized", args: []string{"databus", "list"}, status: 401, code: "AUTH_REQUIRED", exit: 3, calls: 1},
		{name: "forbidden", args: []string{"databus", "query", "--name", "demo"}, status: 403, code: "PERMISSION_DENIED", exit: 3, calls: 1},
		{name: "not found", args: []string{"databus", "query", "--name", "demo"}, status: 404, code: "NOT_FOUND", exit: 4, calls: 1},
		{name: "create conflict", args: []string{"databus", "create", "--name", "demo"}, mutation: 409, code: "CONFLICT", exit: 5, calls: 1},
		{name: "delete rejected", args: []string{"databus", "delete", "--name", "demo"}, mutation: 403, code: "PERMISSION_DENIED", exit: 3, calls: 1},
		{name: "rate limit", args: []string{"databus", "list"}, status: 429, code: "RATE_LIMITED", exit: 1, calls: 1},
		{name: "server failure", args: []string{"databus", "list"}, status: 503, code: "API_ERROR", exit: 1, calls: 1},
		{name: "create terminal error", args: []string{"databus", "create", "--name", "demo"}, resource: `{"status":"error"}`, code: "OPERATION_FAILED", exit: 1, calls: 2},
		{name: "create disappeared", args: []string{"databus", "create", "--name", "demo"}, status: 404, code: "OPERATION_FAILED", exit: 1, calls: 2},
		{name: "delete remains active", args: []string{"databus", "delete", "--name", "demo"}, code: "OPERATION_FAILED", exit: 1, calls: 2},
		{name: "delete polling denied", args: []string{"databus", "delete", "--name", "demo"}, status: 401, code: "AUTH_REQUIRED", exit: 3, calls: 2},
		{name: "delete polling unavailable", args: []string{"databus", "delete", "--name", "demo"}, status: 503, code: "API_ERROR", exit: 1, calls: 2},
		{name: "invalid JSON", args: []string{"databus", "query", "--name", "demo"}, resource: `{broken`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "null JSON", args: []string{"databus", "query", "--name", "demo"}, resource: `null`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "missing list", args: []string{"databus", "list"}, list: `{}`, code: "INVALID_RESPONSE", exit: 1, calls: 1},
		{name: "missing name", args: []string{"databus", "create"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "invalid replicas", args: []string{"databus", "create", "--name", "demo", "--replicas", "0"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "unknown flag", args: []string{"databus", "list", "--bogus"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "unexpected argument", args: []string{"databus", "list", "extra"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "short JSON", args: []string{"databus", "list", "--short"}, code: "INVALID_ARGUMENT", exit: 2},
		{name: "unsupported JSON", args: []string{"databus", "app", "get", "--name", "demo", "--app-name", "app"}, code: "FORMAT_UNSUPPORTED", exit: 2},
		{name: "missing credentials", args: []string{"databus", "list"}, noCredentials: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "JSON alone disables login", args: []string{"databus", "list"}, jsonOnly: true, noCredentials: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "expired credentials", args: []string{"databus", "list"}, expired: true, code: "AUTH_REQUIRED", exit: 3},
		{name: "missing configuration", args: []string{"databus", "list"}, noConfig: true, code: "CONFIG_REQUIRED", exit: 1},
		{name: "default list output", args: []string{"databus", "list"}, text: true, calls: 1},
		{name: "text error on stderr", args: []string{"databus", "query", "--name", "demo"}, status: 404, text: true, exit: 4, calls: 1},
		{name: "text missing credentials", args: []string{"databus", "list"}, text: true, noCredentials: true, exit: 3},
		{name: "configure needs region", args: []string{"configure"}, text: true, exit: 2},
		{name: "login cannot open browser", args: []string{"login"}, text: true, exit: 2},
		{name: "device login cannot open browser", args: []string{"login", "--device"}, text: true, exit: 2},
		{name: "gateway needs project configuration", args: []string{"gateway"}, text: true, exit: 2},
		{name: "spy needs project configuration", args: []string{"spy"}, text: true, exit: 2},
		{name: "edge enrollment needs flags", args: []string{"edge-sync", "agent", "--log-file="}, text: true, exit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/service-accounts/auth/token" {
					if !tc.apiKey || r.Header.Get("X-API-Key") != "test-api-key" {
						t.Errorf("unexpected API key exchange")
					}
					_, _ = w.Write([]byte(`{"access_token":"test-access-token","expires_in":3600}`))
					return
				}
				if r.Header.Get("Authorization") != "Bearer test-access-token" {
					t.Errorf("missing authorization")
				}
				if r.Method == http.MethodPost || r.Method == http.MethodDelete {
					status := tc.mutation
					if status == 0 {
						status = http.StatusCreated
						if r.Method == http.MethodDelete {
							status = http.StatusNoContent
						}
					}
					w.WriteHeader(status)
					if status != http.StatusNoContent {
						body := tc.mutationBody
						if body == "" {
							body = `{"message":"mutation response"}`
						}
						_, _ = w.Write([]byte(body))
					}
					return
				}
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				body := tc.resource
				if body == "" {
					body = `{"name":"demo","status":"active"}`
				}
				if status != http.StatusOK {
					body = `{"message":"API rejected request"}`
				} else if r.URL.Path == "/databuses" {
					body = tc.list
					if body == "" {
						body = `{"databuses":{"demo":{"status":"active","kind":"databus"}}}`
					}
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			home := t.TempDir()
			localAppData := filepath.Join(home, "AppData", "Local")
			root := filepath.Join(rtipaths.Root(home, runtime.GOOS, localAppData), "rticloud")
			writeJSON := func(path string, data any) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.noConfig {
				writeJSON(filepath.Join(root, "configuration", "config.json"), map[string]string{"api_host": server.URL})
			}
			if !tc.noCredentials {
				expiry := time.Now().Add(time.Hour)
				if tc.expired {
					expiry = time.Now().Add(-time.Hour)
				}
				writeJSON(filepath.Join(root, "auth", "credentials.json"), map[string]string{"access_token": "test-access-token", "expires_at": expiry.Format(time.RFC3339Nano)})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestCLIProcess$", "--"}
			if !tc.jsonOnly {
				args = append(args, "--non-interactive")
			}
			args = append(args, tc.args...)
			if !tc.text {
				args = append(args, "--format=json")
			}
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			cmd.Dir = t.TempDir()
			for _, env := range os.Environ() {
				key := strings.SplitN(env, "=", 2)[0]
				if key != "CONNEXT_CLOUD_API_KEY" && key != "LOCALAPPDATA" && !strings.HasPrefix(key, "RTICLOUD_CLI_TEST_") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			cmd.Env = append(cmd.Env, "RTICLOUD_CLI_TEST_CHILD=1", "RTICLOUD_CLI_TEST_HOME="+home, "LOCALAPPDATA="+localAppData)
			if tc.apiKey {
				cmd.Env = append(cmd.Env, "CONNEXT_CLOUD_API_KEY=test-api-key")
			}
			// Keep stdin open without supplying any data. A stray prompt would
			// block, and the process deadline would fail the test.
			input, inputWriter, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer inputWriter.Close()
			cmd.Stdin = input
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("CLI blocked with open stdin: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			exit := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatal(err)
				}
				exit = exitErr.ExitCode()
			}
			if exit != tc.exit || calls.Load() != tc.calls {
				t.Fatalf("exit=%d calls=%d; want exit=%d calls=%d; stdout=%q stderr=%q", exit, calls.Load(), tc.exit, tc.calls, stdout.String(), stderr.String())
			}
			if tc.text {
				if exit != 0 && (stdout.Len() != 0 || stderr.Len() == 0) {
					t.Fatalf("error streams: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				textWant := tc.textWant
				if textWant == "" {
					textWant = `"databuses"`
				}
				if exit == 0 && (!strings.Contains(stdout.String(), textWant) || strings.Contains(stdout.String(), "schema_version") || stderr.Len() != 0) {
					t.Fatalf("default output changed: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				return
			}
			body := stdout.Bytes()
			if exit != 0 {
				if stdout.Len() != 0 {
					t.Fatalf("failure wrote stdout: %q", stdout.String())
				}
				body = stderr.Bytes()
			} else if stderr.Len() != 0 {
				t.Fatalf("success wrote stderr: %q", stderr.String())
			}
			var envelope struct {
				SchemaVersion string          `json:"schema_version"`
				Data          json.RawMessage `json:"data"`
				Error         struct {
					Code      string `json:"code"`
					Retryable bool   `json:"retryable"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("not exactly one JSON document: %s (%v)", body, err)
			}
			if envelope.SchemaVersion != "1" || envelope.Error.Code != tc.code || envelope.Error.Retryable {
				t.Fatalf("unexpected envelope: %s", body)
			}
			if exit == 0 && (len(envelope.Data) == 0 || string(envelope.Data) == "null") {
				t.Fatalf("success has no result: %s", body)
			}
			if exit == 0 && tc.args[1] == "create" && !bytes.Contains(envelope.Data, []byte(`"status":"active"`)) {
				t.Fatalf("creation result is not active: %s", body)
			}
			if exit == 0 && tc.args[1] == "delete" && !bytes.Contains(envelope.Data, []byte(`"status":"deleted"`)) {
				t.Fatalf("deletion result is not deleted: %s", body)
			}
		})
	}
}
