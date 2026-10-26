// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package app

import (
	"bufio"
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/rtipaths"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

// The fake stops at HTTP. Runtime wiring, saved credentials, resource parsing,
// discovery, prompts, config persistence and artifact downloads all run normally.
type setupReply struct {
	status int
	body   any
}
type setupRequest struct {
	method, uri string
	body        map[string]any
}
type setupTransport func(*http.Request) (*http.Response, error)

func (f setupTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type setupFixture struct {
	runtime  *Runtime
	out      bytes.Buffer
	input    *bufio.Reader
	install  string
	replies  map[string][]setupReply
	requests []setupRequest
}

func newSetupFixture(t *testing.T, input string) *setupFixture {
	t.Helper()
	return newSetupFixtureAt(t, input, t.TempDir())
}

func newSetupFixtureAt(t *testing.T, input, workDir string) *setupFixture {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA"} {
		t.Setenv(key, home)
	}
	for _, key := range []string{"NDDSHOME", "CONNEXTDDS_DIR", "RTI_LICENSE_FILE", "CONNEXT_CLOUD_API_KEY"} {
		t.Setenv(key, "")
	}
	oldHome, oldPlatform, oldPaths := connext.UserHomeDir, connext.Platform, rtipaths.UserHomeDir
	connext.UserHomeDir = func() (string, error) { return home, nil }
	connext.Platform = func() (string, string) { return runtime.GOOS, runtime.GOARCH }
	rtipaths.UserHomeDir = connext.UserHomeDir
	t.Cleanup(func() { connext.UserHomeDir, connext.Platform, rtipaths.UserHomeDir = oldHome, oldPlatform, oldPaths })
	oldTransport := http.DefaultTransport
	http.DefaultTransport = setupTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected network outside the Cloud fixture: %s %s", r.Method, r.URL)
		return nil, fmt.Errorf("unexpected network")
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	f := &setupFixture{replies: map[string][]setupReply{}}
	var err error
	f.install, err = connext.ManagedInstallationPath()
	if err != nil {
		t.Fatal(err)
	}
	setupWrite(t, filepath.Join(f.install, "rti_versions.xml"), `<rti><host><base_version>7.7.0.1</base_version><install_dirname>rti_connext_dds-7.7.0</install_dirname></host></rti>`)
	for _, name := range []string{"rtiroutingservice", "rtiddsspy", "rticollectorservicelite"} {
		setupWrite(t, connext.Executable(f.install, name), "fixture executable")
	}
	f.runtime = NewRuntime(workDir, &f.out)
	if err := f.runtime.Config.WriteConfig(map[string]string{"api_host": "https://setup.invalid/api/v1", "region": "us-east-2"}); err != nil {
		t.Fatal(err)
	}
	setupWrite(t, f.runtime.Auth.TokenPath, `{"access_token":"setup-token","expires_at":"2099-01-01T00:00:00Z"}`)
	f.runtime.Auth.NonInteractive = true
	f.input = bufio.NewReader(strings.NewReader(input))
	f.runtime.Gateway.In, f.runtime.Spy.In = f.input, f.input
	f.runtime.CloudAPI.HTTPClient = &http.Client{Transport: setupTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "setup.invalid" || !strings.HasPrefix(r.URL.Path, "/api/v1/") || r.Header.Get("Authorization") != "Bearer setup-token" {
			t.Fatalf("unexpected setup request: %s %s", r.Method, r.URL)
		}
		request := setupRequest{method: r.Method, uri: r.URL.RequestURI()}
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&request.body); err != nil {
				t.Fatal(err)
			}
		}
		f.requests = append(f.requests, request)
		key := r.Method + " " + strings.TrimPrefix(r.URL.RequestURI(), "/api/v1")
		replies := f.replies[key]
		if len(replies) == 0 {
			t.Fatalf("unexpected request: %s", key)
		}
		reply := replies[0]
		if len(replies) > 1 {
			f.replies[key] = replies[1:]
		}
		body, err := json.Marshal(reply.body)
		if err != nil {
			t.Fatal(err)
		}
		status := reply.status
		if status == 0 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	f.reply("GET /databuses?extra_fields=true", map[string]any{"databuses": map[string]any{
		"inventory": map[string]any{"kind": "databus"}, "spare": map[string]any{"kind": "databus"},
		"inventory-obs": map[string]any{"kind": "telemetry"}, "other-obs": map[string]any{"kind": "telemetry"},
	}})
	f.reply("GET /databuses/inventory", map[string]any{"status": common.ServiceStatusActive, "clients": map[string]any{"gw": map[string]any{"kind": "gateway"}, "app_1": map[string]any{"kind": "app"}}, "config": map[string]any{"observability_service": "inventory-obs"}})
	f.reply("GET /databuses/inventory-obs", map[string]any{"status": common.ServiceStatusActive, "clients": map[string]any{"collector": map[string]any{"kind": "telemetry-service-collector"}}})
	f.reply("GET /databuses/inventory/applications/gw", map[string]any{"client_config": "<routing/>"})
	f.reply("GET /databuses/inventory-obs/applications/collector", map[string]any{"client_config": "<collector/>"})
	f.spyBundle("app_1")
	return f
}
func (f *setupFixture) reply(key string, body any) { f.replies[key] = []setupReply{{body: body}} }
func (f *setupFixture) spyBundle(name string) {
	f.reply("GET /databuses/inventory/applications/"+name, map[string]any{"bundle": map[string]any{"version": 2, "qos": setupQoS(name)}})
}
func setupQoS(name string) string {
	return fmt.Sprintf(`<dds><qos_library name="%s_base_qos_lib"><qos_profile name="%s_qos_profile" is_default_qos="true"/></qos_library></dds>`, name, name)
}
func setupWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0700); err != nil {
		t.Fatal(err)
	}
}
func setupRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func (f *setupFixture) posts() []setupRequest {
	var posts []setupRequest
	for _, r := range f.requests {
		if r.method == "POST" {
			posts = append(posts, r)
		}
	}
	return posts
}
func (f *setupFixture) assertInputConsumed(t *testing.T) {
	t.Helper()
	if remaining, err := f.input.Peek(1); err != io.EOF {
		t.Fatalf("setup left scripted answers unread: %q, %v", remaining, err)
	}
}
func setupOrder(t *testing.T, output string, parts ...string) {
	t.Helper()
	previous := -1
	for _, part := range parts {
		at := strings.Index(output, part)
		if at < 0 || at <= previous {
			t.Fatalf("missing or out-of-order %q in:\n%s", part, output)
		}
		previous = at
	}
}

func TestGatewaySetupFromScriptedInput(t *testing.T) {
	for _, tc := range []struct {
		name, input, databus, obs, collector string
		reload, create                       bool
	}{
		{name: "data", input: "2\n1\n1\n", databus: "inventory"},
		{name: "observability", input: "3\n1\n1\n", obs: "inventory-obs", collector: "collector"},
		{name: "reload gateway", input: "2\n1\n1\n1\n", databus: "inventory", reload: true},
		{name: "create collector", input: "3\n1\nnew collector\n", obs: "inventory-obs", collector: "new_collector", create: true},
		{name: "linked observability and generated collector", input: "1\n1\n1\n1\n", databus: "inventory", obs: "inventory-obs", collector: "inventory_gw", create: true},
		{name: "reuse generated collector", input: "1\n1\n1\n1\n", databus: "inventory", obs: "inventory-obs", collector: "inventory_gw"},
		{name: "reuse hashed collector", input: "1\n1\n1\n1\n", databus: strings.Repeat("d", 50), obs: "inventory-obs", collector: strings.Repeat("d", 38) + "_69d6cb22"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSetupFixture(t, tc.input)
			if tc.databus != "" && tc.databus != "inventory" {
				resources := f.replies["GET /databuses?extra_fields=true"][0].body.(map[string]any)["databuses"].(map[string]any)
				resources[tc.databus] = resources["inventory"]
				delete(resources, "inventory")
				for _, suffix := range []string{"", "/applications/gw"} {
					key := "GET /databuses/inventory" + suffix
					f.replies["GET /databuses/"+tc.databus+suffix] = f.replies[key]
					delete(f.replies, key)
				}
			}
			if tc.reload {
				ready := f.replies["GET /databuses/inventory"][0]
				f.replies["GET /databuses/inventory"] = []setupReply{{body: map[string]any{"clients": map[string]any{}}}, ready}
			}
			if tc.collector != "" && tc.collector != "collector" {
				clients := map[string]any{}
				if !tc.create {
					clients[tc.collector] = map[string]any{"kind": "telemetry-service-collector"}
				}
				f.reply("GET /databuses/inventory-obs", map[string]any{"clients": clients})
				f.reply("GET /databuses/inventory-obs/applications/"+tc.collector, map[string]any{"client_config": "<collector/>"})
			}
			if tc.create {
				f.reply("POST /databuses/inventory-obs/applications", map[string]any{})
			}
			values, err := f.runtime.Gateway.ConfigureFirstRun()
			if err != nil {
				t.Fatalf("setup: %v\n%s", err, f.out.String())
			}
			f.assertInputConsumed(t)
			saved, err := f.runtime.Gateway.ReadConfig()
			if err != nil || !reflect.DeepEqual(saved, values) {
				t.Fatalf("saved config: %#v; returned %#v; err %v", saved, values, err)
			}
			if common.StringValue(saved, "databus") != tc.databus || common.StringValue(saved, "observability") != tc.obs || common.NestedString(saved, "runtime", "connext_home") != f.install {
				t.Fatal(saved)
			}
			if tc.databus != "" {
				if common.NestedString(saved, "templates", "gateway") != "gw" || common.NestedString(saved, "clients", "gateway_client_id") == "" {
					t.Fatal(saved)
				}
				if got := setupRead(t, filepath.Join(f.runtime.Gateway.RoutingDir(), "gw.xml")); got != "<routing/>" {
					t.Fatal(got)
				}
			}
			if tc.obs != "" {
				if common.NestedString(saved, "templates", "collector") != tc.collector || common.NestedString(saved, "clients", "collector_client_id") == "" {
					t.Fatal(saved)
				}
				if got := setupRead(t, filepath.Join(f.runtime.Gateway.CollectorDir(), tc.collector+".xml")); got != "<collector/>" {
					t.Fatal(got)
				}
			}
			posts := f.posts()
			if tc.create {
				if len(posts) != 1 || posts[0].uri != "/api/v1/databuses/inventory-obs/applications" || !reflect.DeepEqual(posts[0].body, map[string]any{"kind": "telemetry-service-collector", "client_name": tc.collector}) {
					t.Fatal(posts)
				}
			} else if len(posts) != 0 {
				t.Fatalf("unexpected create: %+v", posts)
			}
			output := tui.StripANSIEscapes(f.out.String())
			setupOrder(t, output, "Connext Cloud Gateway setup", "Using Connext Pro 7.7.0.1", "Select gateway capability:", "Configuration saved to", "Downloaded")
			if tc.reload {
				setupOrder(t, output, "Create an Edge Gateway application", "Reloading templates...", "Configuration saved to")
			}
			if tc.databus != "" && tc.obs != "" && !strings.Contains(output, fmt.Sprintf("%s  (linked to %s)", tc.obs, tc.databus)) {
				t.Fatal(output)
			}
			if strings.Contains(output, "Install required Connext components") {
				t.Fatal("healthy managed installation prompted for installation")
			}
		})
	}
}

func TestSpySetupFromScriptedInput(t *testing.T) {
	for _, tc := range []struct {
		name, input, app         string
		reload, create, existing bool
	}{
		{name: "existing application", input: "1\n1\n", app: "app_1"},
		{name: "reload applications", input: "1\n2\n1\n1\n", app: "app_1", reload: true},
		{name: "create spy application", input: "1\n2\n", app: spy.RTICloudSpyAppName, create: true},
		{name: "reuse spy application", input: "1\n2\n", app: spy.RTICloudSpyAppName, existing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSetupFixture(t, tc.input)
			if tc.reload {
				ready := f.replies["GET /databuses/inventory"][0]
				f.replies["GET /databuses/inventory"] = []setupReply{{body: map[string]any{"clients": map[string]any{}}}, ready}
			}
			if tc.existing {
				f.reply("GET /databuses/inventory", map[string]any{"clients": map[string]any{"app_1": map[string]any{"kind": "app"}, spy.RTICloudSpyAppName: map[string]any{"kind": "app"}}})
			}
			if tc.create {
				f.reply("POST /databuses/inventory/applications", map[string]any{})
			}
			f.spyBundle(tc.app)
			values, err := f.runtime.Spy.ConfigureFirstRun()
			if err != nil {
				t.Fatalf("setup: %v\n%s", err, f.out.String())
			}
			f.assertInputConsumed(t)
			saved, err := f.runtime.Spy.ReadConfig()
			if err != nil || !reflect.DeepEqual(saved, values) {
				t.Fatalf("saved config: %#v; returned %#v; err %v", saved, values, err)
			}
			if common.StringValue(saved, "databus") != "inventory" || common.NestedString(saved, "templates", "app") != tc.app || common.NestedString(saved, "runtime", "connext_home") != f.install || common.NestedString(saved, "clients", "app_client_id") == "" {
				t.Fatal(saved)
			}
			if got := setupRead(t, filepath.Join(f.runtime.Spy.AppDir(), "generated", tc.app+".qos.xml")); got != setupQoS(tc.app) {
				t.Fatal(got)
			}
			if err := f.runtime.Spy.ValidateLocalArtifacts(saved); err != nil {
				t.Fatal(err)
			}
			posts := f.posts()
			if tc.create {
				want := map[string]any{"kind": "app", "client_name": spy.RTICloudSpyAppName, "port": float64(spy.RTICloudSpyAppPort), "topic_data": map[string]any{"0": map[string]any{"domainId": float64(0), "tag": "", "configuration": "all", "allTopicsConfiguration": map[string]any{"cloudToEdgeDirection": true, "cloudToEdgeHistory": float64(1), "edgeToCloudDirection": false, "edgeToCloudHistory": float64(1)}}}}
				if len(posts) != 1 || posts[0].uri != "/api/v1/databuses/inventory/applications" || !reflect.DeepEqual(posts[0].body, want) {
					t.Fatalf("unexpected create request: %+v", posts)
				}
			} else if len(posts) != 0 {
				t.Fatalf("unexpected create: %+v", posts)
			}
			output := tui.StripANSIEscapes(f.out.String())
			setupOrder(t, output, "Connext Cloud Spy setup", "Using Connext Pro 7.7.0.1", "Select Databus:", "Select Cloud Native application", "Configuration saved to", "Downloaded cloud application template")
			if tc.reload {
				setupOrder(t, output, "Create Cloud Native application in Connext Cloud dashboard", "Reloading applications...", "Configuration saved to")
			}
			if tc.existing && strings.Contains(output, spy.CreateRTICloudSpyApp) {
				t.Fatal("offered creation of existing Spy application")
			}
		})
	}
}

func TestSetupCancellationDoesNotSaveProject(t *testing.T) {
	for _, tc := range []struct {
		name, command, input, prompt, errorText string
		missing                                 bool
	}{
		{name: "gateway capability EOF", command: "gateway", prompt: "Select gateway capability:", errorText: "Gateway configuration cancelled."},
		{name: "gateway template EOF", command: "gateway", input: "2\n1\n", prompt: "Select Gateway template from inventory:", errorText: "Gateway configuration cancelled."},
		{name: "gateway dashboard cancel", command: "gateway", input: "2\n1\n2\n2\n", prompt: "After you've created the gateway application in the dashboard, reload.", errorText: "Gateway configuration cancelled."},
		{name: "collector name EOF", command: "gateway", input: "3\n1\n2\n", prompt: "Collector name", errorText: "Gateway configuration cancelled."},
		{name: "spy application EOF", command: "spy", input: "1\n", prompt: "Select Cloud Native application", errorText: "Spy configuration cancelled."},
		{name: "spy dashboard cancel", command: "spy", input: "1\n3\n2\n", prompt: "After you've created the Cloud Native application in the dashboard, reload.", errorText: "Spy configuration cancelled."},
		{name: "missing installation", command: "gateway", input: "\n", missing: true, prompt: "Continue with installation?", errorText: "Connext installation cancelled."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSetupFixture(t, tc.input)
			if tc.missing {
				if err := os.RemoveAll(f.install); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if tc.command == "gateway" {
				_, err = f.runtime.Gateway.ConfigureFirstRun()
			} else {
				_, err = f.runtime.Spy.ConfigureFirstRun()
			}
			if err == nil || !strings.HasPrefix(err.Error(), tc.errorText) {
				t.Fatalf("setup error=%v; want %q\n%s", err, tc.errorText, f.out.String())
			}
			if !strings.Contains(tui.StripANSIEscapes(f.out.String()), tc.prompt) {
				t.Fatalf("setup did not reach %q:\n%s", tc.prompt, f.out.String())
			}
			f.assertInputConsumed(t)
			root := filepath.Join(f.runtime.Gateway.WorkDir, ".connext")
			if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
				t.Fatalf("cancelled setup wrote project files: %v", statErr)
			}
			if len(f.posts()) != 0 {
				t.Fatal("cancelled setup created an application")
			}
			if tc.missing && strings.Contains(f.out.String(), "Select gateway capability:") {
				t.Fatal("selected capability before Connext discovery succeeded")
			}
		})
	}
}

func TestSetupReportsCloudFailureWithoutWritingArtifact(t *testing.T) {
	for _, command := range []string{"gateway", "spy"} {
		for _, stage := range []string{"resource", "artifact"} {
			t.Run(command+"/"+stage, func(t *testing.T) {
				input := "2\n1\n1\n"
				if command == "spy" {
					input = "1\n1\n"
				}
				f := newSetupFixture(t, input)
				route := "GET /databuses/inventory"
				artifactPath := filepath.Join(f.runtime.Gateway.RoutingDir(), "gw.xml")
				configPath := f.runtime.Gateway.ConfigPath()
				if stage == "artifact" {
					route += "/applications/gw"
				}
				if command == "spy" {
					artifactPath = filepath.Join(f.runtime.Spy.AppDir(), "generated", "app_1.qos.xml")
					configPath = f.runtime.Spy.ConfigPath()
					if stage == "artifact" {
						route = "GET /databuses/inventory/applications/app_1"
					}
				}
				f.replies[route] = []setupReply{{status: http.StatusServiceUnavailable, body: map[string]any{"error": "fixture service unavailable"}}}
				var err error
				if command == "gateway" {
					_, err = f.runtime.Gateway.ConfigureFirstRun()
				} else {
					_, err = f.runtime.Spy.ConfigureFirstRun()
				}
				if err == nil {
					t.Fatal("setup ignored Cloud failure")
				}
				if _, statErr := os.Stat(artifactPath); !os.IsNotExist(statErr) {
					t.Fatalf("failed download wrote artifact: %v", statErr)
				}
				_, statErr := os.Stat(configPath)
				if stage == "resource" && !os.IsNotExist(statErr) {
					t.Fatalf("failed resource lookup saved configuration: %v", statErr)
				}
				if stage == "artifact" && statErr != nil {
					t.Fatalf("selected configuration should remain available for retry: %v", statErr)
				}
				if strings.Contains(f.out.String(), "Downloaded") {
					t.Fatalf("failed setup reported a download: %s", f.out.String())
				}
			})
		}
	}
}

func TestSetupRegistersAndReusesRealSecureCredentials(t *testing.T) {
	for _, tc := range []struct {
		command        string
		nested, linked bool
	}{
		{command: "gateway"}, {command: "collector"}, {command: "spy"},
		{command: "gateway", nested: true}, {command: "collector", nested: true}, {command: "spy", nested: true},
		{command: "gateway", linked: true}, {command: "collector", linked: true}, {command: "spy", linked: true},
	} {
		shape := "top-level"
		if tc.nested {
			shape = "nested"
		}
		if tc.linked {
			shape = "linked project"
		}
		t.Run(tc.command+"/"+shape, func(t *testing.T) {
			command := tc.command
			input, resource, template, clientField := "2\n1\n1\n", "inventory", "gw", "gateway_client_id"
			if command == "collector" {
				input, resource, template, clientField = "3\n1\n1\n", "inventory-obs", "collector", "collector_client_id"
			}
			if command == "spy" {
				input, template, clientField = "1\n1\n", "app_1", "app_client_id"
			}
			workDir := t.TempDir()
			if tc.linked {
				selected := filepath.Join(t.TempDir(), "project")
				if err := os.Symlink(workDir, selected); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				workDir = selected
			}
			f := newSetupFixtureAt(t, input, workDir)
			body := f.replies["GET /databuses/"+resource][0].body.(map[string]any)
			if tc.nested {
				config, _ := body["config"].(map[string]any)
				if config == nil {
					config = map[string]any{}
					body["config"] = config
				}
				config["secure"] = true
			} else {
				body["secure"] = true
			}
			files := []string{"client.crt", "identity_ca.crt", "permissions_ca.crt", "governance.p7s", "permissions.p7s", "psk.key"}
			targetDir := f.runtime.Gateway.RoutingDir()
			if command == "collector" {
				targetDir = filepath.Join(f.runtime.Gateway.CollectorDir(), "secure")
			}
			if command == "spy" {
				targetDir = filepath.Join(f.runtime.Spy.AppDir(), "generated", "security")
				files[3], files[4] = "signed_governance.p7s", "signed_permissions.p7s"
			}
			secureFiles := map[string]any{}
			for _, name := range files {
				secureFiles[name] = base64.StdEncoding.EncodeToString([]byte("fixture " + name))
			}
			route := "/databuses/" + resource + "/applications/" + template + "/clients"
			f.reply("POST "+route, map[string]any{"secure_files": secureFiles})
			var values map[string]any
			var err error
			if command == "spy" {
				values, err = f.runtime.Spy.ConfigureFirstRun()
			} else {
				values, err = f.runtime.Gateway.ConfigureFirstRun()
			}
			if err != nil {
				t.Fatal(err)
			}
			ensure := func() {
				t.Helper()
				if command == "spy" {
					secure, err := f.runtime.Spy.EnsureSecureArtifacts(values)
					if err != nil || !secure {
						t.Fatalf("secure Spy: %v, %v", secure, err)
					}
				} else {
					data, collector, err := f.runtime.Gateway.EnsureSecureArtifacts(values)
					if err != nil || data != (command == "gateway") || collector != (command == "collector") {
						t.Fatalf("secure Gateway: %v, %v, %v", data, collector, err)
					}
				}
			}
			ensure()
			posts := f.posts()
			if len(posts) != 1 || posts[0].uri != "/api/v1"+route || posts[0].body["client_id"] != common.NestedString(values, "clients", clientField) {
				t.Fatalf("registration: %+v", posts)
			}
			csrPEM, _ := posts[0].body["csr"].(string)
			block, _ := pem.Decode([]byte(csrPEM))
			if block == nil {
				t.Fatal("registration did not contain a PEM CSR")
			}
			csr, err := x509.ParseCertificateRequest(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if err := csr.CheckSignature(); err != nil {
				t.Fatalf("CSR signature: %v", err)
			}
			if csr.Subject.CommonName != resource+"."+template+"."+common.NestedString(values, "clients", clientField) {
				t.Fatal(csr.Subject)
			}
			privatePEM := setupRead(t, filepath.Join(targetDir, "client.key"))
			keyBlock, _ := pem.Decode([]byte(privatePEM))
			if keyBlock == nil {
				t.Fatal("saved private key is not PEM")
			}
			key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			if !key.PublicKey.Equal(csr.PublicKey) {
				t.Fatal("saved key does not match registration CSR")
			}
			before := map[string]os.FileInfo{}
			for _, name := range append(files, "client.key") {
				path := filepath.Join(targetDir, name)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				before[name] = info
				mode := os.FileMode(0644)
				if strings.HasSuffix(name, ".key") {
					mode = 0600
				}
				if permissions := info.Mode().Perm(); runtime.GOOS != "windows" && (permissions&0600 != 0600 || permissions&^mode != 0) {
					t.Fatalf("%s mode %o; need owner read/write and no permissions beyond %o", name, permissions, mode)
				}
				if name != "client.key" && setupRead(t, path) != "fixture "+name {
					t.Fatalf("wrong decoded %s", name)
				}
			}
			ensure()
			if len(f.posts()) != 1 {
				t.Fatal("reused credentials triggered another registration")
			}
			for name, info := range before {
				path := filepath.Join(targetDir, name)
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
					t.Fatalf("reused %s was rewritten: %v", name, err)
				}
			}
			if setupRead(t, filepath.Join(targetDir, "client.key")) != privatePEM {
				t.Fatal("reused key changed")
			}
			setupOrder(t, tui.StripANSIEscapes(f.out.String()), "Configuration saved to", "Registered "+command+" client credentials", "Saved "+command+" credentials to", "Reusing local "+command+" credentials")
			f.assertInputConsumed(t)
		})
	}
}

func TestFreshSetupLaunchesSelectedConnextTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable fixtures")
	}
	for _, command := range []string{"gateway", "collector", "spy"} {
		t.Run(command, func(t *testing.T) {
			input, tool := "2\n1\n1\n", "rtiroutingservice"
			if command == "collector" {
				input, tool = "3\n1\n1\n", "rticollectorservicelite"
			}
			if command == "spy" {
				input, tool = "1\n1\n", "rtiddsspy"
			}
			f := newSetupFixture(t, input)
			license := filepath.Join(t.TempDir(), "rti_license.dat")
			setupWrite(t, license, "FEATURE RTIPRO RTI 4. permanent TEST\nFEATURE RTISECURITY RTI 5. permanent TEST\nFEATURE RTIRWT RTI 6. permanent TEST\n")
			t.Setenv("RTI_LICENSE_FILE", license)
			record := filepath.Join(t.TempDir(), "process.txt")
			t.Setenv("RTICLOUD_TEST_PROCESS_RECORD", record)
			script := `#!/bin/sh
if [ "$1" = "-help" ]; then echo 'fixture help'; exit 0; fi
next_file=false
for argument in "$@"; do
 if [ "$next_file" = true ]; then [ -s "$argument" ] || exit 41; next_file=false; fi
 case "$argument" in -cfgFile|-qosFile) next_file=true;; esac
done
printf '%s\n' "$NDDSHOME" "$RTI_LICENSE_FILE" "$@" > "$RTICLOUD_TEST_PROCESS_RECORD"
printf 'setup-process-ready\n'
`
			setupWrite(t, connext.Executable(f.install, tool), script)
			var err error
			if command == "spy" {
				err = f.runtime.RunSpy("text", false)
			} else {
				err = f.runtime.RunGateway("text", false)
			}
			if err != nil {
				t.Fatalf("run: %v\n%s", err, f.out.String())
			}
			f.assertInputConsumed(t)
			lines := strings.Split(strings.TrimSpace(setupRead(t, record)), "\n")
			if len(lines) < 3 || lines[0] != f.install || lines[1] != license {
				t.Fatalf("selected process environment: %q", lines)
			}
			var logPath, artifact string
			var state map[string]any
			switch command {
			case "gateway":
				logPath, artifact = f.runtime.Gateway.RoutingLogPath(), filepath.Join(f.runtime.Gateway.RoutingDir(), "gw.xml")
				state, err = f.runtime.Gateway.RuntimeState()
			case "collector":
				logPath, artifact = f.runtime.Gateway.CollectorLogPath(), filepath.Join(f.runtime.Gateway.CollectorDir(), "collector.xml")
				state, err = f.runtime.Gateway.RuntimeState()
			case "spy":
				logPath, artifact = f.runtime.Spy.SpyLogPath(), filepath.Join(f.runtime.Spy.AppDir(), "generated", "app_1.qos.xml")
				state, err = f.runtime.Spy.RuntimeState()
			}
			if err != nil || common.StringValue(state, "connext_home") != f.install {
				t.Fatalf("runtime state: %#v, %v", state, err)
			}
			if !strings.Contains(strings.Join(lines[2:], "\n"), artifact) {
				t.Fatalf("process did not receive downloaded artifact: %q", lines)
			}
			if log := setupRead(t, logPath); !strings.Contains(log, "setup-process-ready") {
				t.Fatalf("process output missing from log: %s", log)
			}
		})
	}
}

// Exercise preflight through real Runtime wiring and HTTP adapters. Rejection
// must preserve existing credentials and avoid a registration request.
func TestProjectCredentialPreflightBeforeRegistration(t *testing.T) {
	for _, command := range []string{"gateway", "collector", "spy"} {
		t.Run(command, func(t *testing.T) {
			f := newSetupFixture(t, "")
			resource := "inventory"
			values := map[string]any{"databus": resource, "templates": map[string]any{"gateway": "gw", "app": "app_1"}}
			target := f.runtime.Gateway.RoutingDir()
			if command == "collector" {
				resource = "inventory-obs"
				values = map[string]any{"observability": resource, "templates": map[string]any{"collector": "collector"}}
				target = filepath.Join(f.runtime.Gateway.CollectorDir(), "secure")
			}
			if command == "spy" {
				target = filepath.Join(f.runtime.Spy.AppDir(), "generated", "security")
			}
			f.reply("GET /databuses/"+resource, map[string]any{"secure": true})
			if err := os.MkdirAll(filepath.Join(target, "client.key"), 0700); err != nil {
				t.Fatal(err)
			}
			setupWrite(t, filepath.Join(target, "client.crt"), "existing certificate")
			var err error
			if command == "spy" {
				_, err = f.runtime.Spy.EnsureSecureArtifacts(values)
			} else {
				_, _, err = f.runtime.Gateway.EnsureSecureArtifacts(values)
			}
			if !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("want unsafe output error, got %v", err)
			}
			if len(f.posts()) != 0 {
				t.Fatal("invalid credential destination registered a client")
			}
			if got := setupRead(t, filepath.Join(target, "client.crt")); got != "existing certificate" {
				t.Fatalf("certificate changed: %q", got)
			}
			if entries, err := os.ReadDir(target); err != nil || len(entries) != 2 {
				t.Fatalf("preflight changed outputs: %v, %v", entries, err)
			}
		})
	}
}
