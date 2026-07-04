// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package spy

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

func TestSpyStateParsesTopicsSamplesAndStatistics(t *testing.T) {
	state := NewSpyState(5)
	lines := []string{
		"rtiddsspy is listening for data, press CTRL+C to stop it.",
		`2026-05-26T20:31:35Z [spy] 2026-05-26 20:24:38.405954 New participant    from 172.31.38.177  : name="RTI Persistence Service: core: defaultParticipant" hostName="rti-persistence-6d58996bf5-76728" processId="123"`,
		`2026-05-26 20:24:39.405954 New participant    from 172.31.38.178  : name="Shape Publisher" hostName="shape-host" processId="456"`,
		`2026-05-26 20:24:40.405954 New participant    from 172.31.38.179  : name="Shape Reader" hostName="shape-host" processId="789"`,
		`2026-05-05 14:55:51.771722 New writer        from 10.0.0.114     : topic="Square" type="ShapeType"`,
		`2026-05-05 14:55:51.772522 New reader        from 10.0.0.114     : topic="Square" type="ShapeType"`,
		`2026-05-08 00:06:58.475269 New data          from 10.0.0.114     : topic="Square" type="ShapeType" sample={"color":"BLUE","x":84}`,
		`2026-05-08 00:06:58.526430 Modified instance from 10.0.0.114     : topic="Square" type="ShapeType" sample={"color":"RED","x":78}`,
		"---- Statistics ----",
		"Discovered 5 DataWriters and 1 DataReaders",
		`	26, 0, 0 	(Topic="Square"  Type="ShapeType")`,
	}
	for _, line := range lines {
		state.Update(line)
	}
	rows := state.TopicRows()
	if len(rows) != 1 || rows[0].Topic != "Square" || rows[0].Writers != 1 || rows[0].Readers != 1 || rows[0].Samples != 26 || rows[0].LatestTime != "2026-05-08 00:06:58.526430" || rows[0].LatestJSON != `{"color":"RED","x":78}` {
		t.Fatalf("unexpected rows: %#v", rows)
	}
	writers, readers := state.EndpointTotals()
	if writers != 5 || readers != 1 {
		t.Fatalf("unexpected endpoint totals: %d %d", writers, readers)
	}
	if state.ServiceState() != "stopped" {
		t.Fatalf("unexpected service state: %s", state.ServiceState())
	}
	hosts := state.ConnectedHostNames()
	if strings.Join(hosts, ",") != "rti-persistence-6d58996bf5-76728,shape-host" {
		t.Fatalf("unexpected connected hosts: %#v", hosts)
	}
}

func TestPlainEventLinesFormatsSpyEvents(t *testing.T) {
	lines := []string{
		`2026-05-05 14:55:51.771722 New writer        from 10.0.0.114     : topic="Square" type="ShapeType"`,
		`2026-05-08 00:06:58.475269 New data          from 10.0.0.114     : topic="Square" type="ShapeType" sample={"color":"BLUE","x":84}`,
		"Discovered 5 DataWriters and 1 DataReaders",
		`	26, 0, 0 	(Topic="Square"  Type="ShapeType")`,
	}
	got := []string{}
	for _, line := range lines {
		got = append(got, PlainEventLines(line)...)
	}
	expected := []string{
		"[writer] Square ShapeType from 10.0.0.114",
		`[data] Square ShapeType {"color":"BLUE","x":84}`,
		"[stats] writers=5 readers=1",
		"[stats] Square ShapeType data=26 dispose=0 no_writers=0",
	}
	if strings.Join(got, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected lines:\n%s", strings.Join(got, "\n"))
	}
}

func TestRenderSetupIntroUsesGatewayStylePanel(t *testing.T) {
	rendered := RenderSetupIntro(3)
	checks := []string{"╭", "Connext Cloud Spy setup", "Create a project-local spy configuration for this workspace.", "Databuses available: 3", "Use arrow keys to choose and Enter to confirm.", "╰"}
	for _, check := range checks {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing %q in %s", check, rendered)
		}
	}
}

func TestSpyStateRemovesDeletedParticipants(t *testing.T) {
	state := NewSpyState(5)
	state.Update(`2026-05-26T20:31:35Z [spy] 2026-05-26 20:24:38.405954 New participant    from 172.31.38.177  : name="RTI Persistence Service: core: defaultParticipant" hostName="rti-persistence-6d58996bf5-76728" processId="123"`)
	state.Update(`2026-05-26 20:24:39.405954 New participant    from 172.31.38.178  : name="Shape Publisher" hostName="shape-host" processId="456"`)
	state.Update(`2026-05-26T20:39:48Z [spy] xxxx-xx-xx xx:xx:xx.xxxxxx Deleted participant from 172.31.38.177  : name="RTI Persistence Service: core: defaultParticipant" hostName="rti-persistence-6d58996bf5-76728" processId="123"`)

	hosts := state.ConnectedHostNames()
	if strings.Join(hosts, ",") != "shape-host" {
		t.Fatalf("unexpected connected hosts after delete: %#v", hosts)
	}
}

func TestQosProfileFromXMLSelectsDefaultNativeAppProfile(t *testing.T) {
	tmpDir := t.TempDir()
	xmlPath := filepath.Join(tmpDir, "app_1.xml")
	if err := os.WriteFile(xmlPath, []byte(`
<dds>
  <qos_library name="app_1_qos_lib">
    <qos_profile name="cloud"/>
    <qos_profile name="app_1_qos_profile" base_name="cloud" is_default_qos="true"/>
  </qos_library>
</dds>`), 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := QosProfileFromXML(xmlPath, "app_1")
	if err != nil {
		t.Fatal(err)
	}
	if profile != "app_1_qos_lib::app_1_qos_profile" {
		t.Fatalf("unexpected profile: %s", profile)
	}
}

func TestConfigureFirstRunPromptsForDatabusAndCloudNativeApp(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		return map[string]map[string]any{"inventory": {}}, map[string]map[string]any{"obs": {}}, nil
	}
	resourceCalls := 0
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		resourceCalls++
		if resourceCalls == 1 {
			return map[string]any{"name": "inventory", "clients": map[string]any{}}, nil
		}
		return map[string]any{"name": "inventory", "clients": map[string]any{"app_1": map[string]any{"kind": "app"}, "gw": map[string]any{"kind": "gateway"}}}, nil
	}
	app.DiscoverConnextInstallFn = func(prompt bool) (ConnextInstall, error) {
		return ConnextInstall{Path: install, Version: "7.7.0"}, nil
	}
	app.DownloadArtifactsFunc = func(config map[string]any, force bool) error { return nil }
	app.ConfirmReloadFunc = func(message string) (bool, error) {
		if message != "After you've created the Cloud Native application in the dashboard, reload." {
			return false, UserError{Message: message}
		}
		return true, nil
	}
	appSelectCalls := 0
	app.SelectFunc = func(message string, choices []string) (string, error) {
		switch message {
		case "Select Databus:":
			return "inventory", nil
		case "Select Cloud Native application from inventory:":
			appSelectCalls++
			if appSelectCalls == 1 {
				return CreateNewApp, nil
			}
			return "app_1", nil
		default:
			return "", UserError{Message: message}
		}
	}
	config, err := app.ConfigureFirstRun(true)
	if err != nil {
		t.Fatal(err)
	}
	if stringValue(config, "databus") != "inventory" || common.NestedString(config, "templates", "app") != "app_1" || common.NestedString(config, "runtime", "connext_home") != install {
		t.Fatalf("unexpected config: %#v", config)
	}
	output := out.String()
	if !strings.Contains(output, "Connext Cloud Spy setup") || !strings.Contains(output, "Create Cloud Native application") || !strings.Contains(output, "Configuration saved to "+filepath.Join(tmpDir, ".connext", "spy.yaml")) {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestConfigureFirstRunCanCreateRTICloudSpyApp(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		return map[string]map[string]any{"inventory": {}}, nil, nil
	}
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": "inventory", "clients": map[string]any{"app_1": map[string]any{"kind": "app"}}}, nil
	}
	app.DiscoverConnextInstallFn = func(prompt bool) (ConnextInstall, error) {
		return ConnextInstall{Path: install, Version: "7.7.0"}, nil
	}
	app.DownloadArtifactsFunc = func(config map[string]any, force bool) error { return nil }
	app.APIPost = func(path string, payload map[string]any) (map[string]any, error) {
		if path != "/databuses/inventory/applications" {
			return nil, UserError{Message: path}
		}
		if payload["kind"] != "app" || payload["client_name"] != RTICloudSpyAppName || payload["port"] != RTICloudSpyAppPort {
			t.Fatalf("unexpected create payload: %#v", payload)
		}
		topicData, ok := payload["topic_data"].(map[string]any)
		if !ok {
			t.Fatalf("missing topic_data: %#v", payload)
		}
		domain, ok := topicData["0"].(map[string]any)
		if !ok || domain["configuration"] != "all" || domain["domainId"] != 0 || domain["tag"] != "" {
			t.Fatalf("unexpected topic domain: %#v", topicData)
		}
		allTopics, ok := domain["allTopicsConfiguration"].(map[string]any)
		if !ok || allTopics["cloudToEdgeDirection"] != true || allTopics["edgeToCloudDirection"] != false {
			t.Fatalf("unexpected topic permissions: %#v", domain)
		}
		return map[string]any{}, nil
	}
	app.SelectFunc = func(message string, choices []string) (string, error) {
		switch message {
		case "Select Databus:":
			return "inventory", nil
		case "Select Cloud Native application from inventory:":
			if !containsString(choices, CreateRTICloudSpyApp) {
				t.Fatalf("expected create rticloud_spy choice in %#v", choices)
			}
			return CreateRTICloudSpyApp, nil
		default:
			return "", UserError{Message: message}
		}
	}
	config, err := app.ConfigureFirstRun(true)
	if err != nil {
		t.Fatal(err)
	}
	if common.NestedString(config, "templates", "app") != RTICloudSpyAppName {
		t.Fatalf("unexpected config: %#v", config)
	}
	if !strings.Contains(out.String(), "Created rticloud_spy cloud application") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestConfigureFirstRunDoesNotOfferCreateWhenRTICloudSpyExists(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		return map[string]map[string]any{"inventory": {}}, nil, nil
	}
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": "inventory", "clients": map[string]any{
			RTICloudSpyAppName: map[string]any{"kind": "app"},
			"app_1":            map[string]any{"kind": "app"},
		}}, nil
	}
	app.DiscoverConnextInstallFn = func(prompt bool) (ConnextInstall, error) {
		return ConnextInstall{Path: install, Version: "7.7.0"}, nil
	}
	app.DownloadArtifactsFunc = func(config map[string]any, force bool) error { return nil }
	app.APIPost = func(path string, payload map[string]any) (map[string]any, error) {
		t.Fatalf("did not expect create call: %s %#v", path, payload)
		return nil, nil
	}
	app.SelectFunc = func(message string, choices []string) (string, error) {
		switch message {
		case "Select Databus:":
			return "inventory", nil
		case "Select Cloud Native application from inventory:":
			if containsString(choices, CreateRTICloudSpyApp) {
				t.Fatalf("did not expect create rticloud_spy choice in %#v", choices)
			}
			return RTICloudSpyAppName, nil
		default:
			return "", UserError{Message: message}
		}
	}
	config, err := app.ConfigureFirstRun(true)
	if err != nil {
		t.Fatal(err)
	}
	if common.NestedString(config, "templates", "app") != RTICloudSpyAppName {
		t.Fatalf("unexpected config: %#v", config)
	}
}

func TestValidateConfigResourcesCreatesMissingConfiguredRTICloudSpyApp(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": name, "status": common.ServiceStatusActive, "clients": map[string]any{"app_1": map[string]any{"kind": "app"}}}, nil
	}
	created := false
	app.APIPost = func(path string, payload map[string]any) (map[string]any, error) {
		if path != "/databuses/test-new/applications" || payload["client_name"] != RTICloudSpyAppName {
			t.Fatalf("unexpected create call: %s %#v", path, payload)
		}
		created = true
		return map[string]any{}, nil
	}
	config := map[string]any{"databus": "test-new", "templates": map[string]any{"app": RTICloudSpyAppName}}
	if err := app.ValidateConfigResources(config); err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected rticloud_spy app to be created")
	}
}

func TestValidateConfigResourcesRejectsInactiveSpyDatabus(t *testing.T) {
	app := NewApp(t.TempDir(), &bytes.Buffer{})
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": name, "status": common.ServiceStatusDisabled, "clients": map[string]any{"app_1": map[string]any{"kind": "app"}}}, nil
	}
	config := map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}}
	err := app.ValidateConfigResources(config)
	if err == nil {
		t.Fatal("expected inactive databus error")
	}
	message := err.Error()
	if !strings.Contains(message, "Databus 'db' is disabled, not active") ||
		!strings.Contains(message, "\x1b[33m⚠\x1b[0m") ||
		!strings.Contains(message, "rticloud databus resume --name db") {
		t.Fatalf("unexpected error: %s", message)
	}
}

func TestValidateLocalArtifactsRejectsMissingSpyXML(t *testing.T) {
	app := NewApp(t.TempDir(), &bytes.Buffer{})
	config := map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}}
	err := app.ValidateLocalArtifacts(config)
	if err == nil {
		t.Fatal("expected missing local artifact error")
	}
	if !strings.Contains(err.Error(), "Local spy artifact was not found") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestDownloadArtifactsWritesCloudApplicationXML(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	app.APIGet = func(path string) (map[string]any, error) {
		if path != "/databuses/inventory/applications/app_1" {
			return nil, UserError{Message: path}
		}
		return map[string]any{"client_config": "<dds/>"}, nil
	}
	config := map[string]any{"databus": "inventory", "templates": map[string]any{"app": "app_1"}}
	if err := app.DownloadArtifacts(config, true); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(tmpDir, ".connext", "spy", "app", "app_1.xml")); got != "<dds/>" {
		t.Fatalf("unexpected app xml: %s", got)
	}
	if !strings.Contains(out.String(), "✓") || !strings.Contains(out.String(), "Downloaded cloud application template") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDownloadArtifactsExplainsMissingDatabusEndpoint(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	app.APIGet = func(path string) (map[string]any, error) {
		return nil, UserError{Message: "Error: Databus 'cdb-db-id' doesn't have an external endpoint configured."}
	}
	config := map[string]any{"databus": "test-new", "templates": map[string]any{"app": RTICloudSpyAppName}}
	err := app.DownloadArtifacts(config, true)
	if err == nil {
		t.Fatal("expected error")
	}
	message := err.Error()
	if !strings.Contains(message, "Databus 'test-new' does not have an external endpoint yet") ||
		!strings.Contains(message, "rticloud databus resume --name test-new") ||
		!strings.Contains(message, "rticloud spy") {
		t.Fatalf("unexpected error: %s", message)
	}
}

func TestSecureSpyCredentialsSavedNextToAppXML(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": name, "config": map[string]any{"secure": true}}, nil
	}
	app.GenerateCSRFunc = func(resource string, template string, clientID string) ([]byte, string, error) {
		return []byte("private-key"), "csr", nil
	}
	app.APIPost = func(path string, payload map[string]any) (map[string]any, error) {
		if path != "/databuses/db/applications/app_1/clients" || payload["client_id"] != "app_1-1" {
			return nil, UserError{Message: path}
		}
		return map[string]any{"secure_files": map[string]any{
			"client.crt":             base64.StdEncoding.EncodeToString([]byte("cert")),
			"identity_ca.crt":        base64.StdEncoding.EncodeToString([]byte("identity")),
			"permissions_ca.crt":     base64.StdEncoding.EncodeToString([]byte("permissions")),
			"signed_governance.p7s":  base64.StdEncoding.EncodeToString([]byte("governance")),
			"signed_permissions.p7s": base64.StdEncoding.EncodeToString([]byte("signed")),
			"psk.key":                base64.StdEncoding.EncodeToString([]byte("psk")),
		}}, nil
	}
	config := map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}, "clients": map[string]any{"app_client_id": "app_1-1"}}
	secure, err := app.EnsureSecureArtifacts(config)
	if err != nil {
		t.Fatal(err)
	}
	if !secure {
		t.Fatal("expected secure databus")
	}
	if got := readFile(t, filepath.Join(tmpDir, ".connext", "spy", "app", "client.key")); got != "private-key" {
		t.Fatalf("unexpected client.key: %s", got)
	}
	if got := readFile(t, filepath.Join(tmpDir, ".connext", "spy", "app", "client.crt")); got != "cert" {
		t.Fatalf("unexpected client.crt: %s", got)
	}
	output := out.String()
	if !strings.Contains(output, "•") || !strings.Contains(output, "Secure Databus detected.") || strings.Count(output, "✓") < 2 || !strings.Contains(output, "Registered spy client credentials") || !strings.Contains(output, "Saved spy credentials to ") {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestValidateConnextRequiresRTIDdsSpy77(t *testing.T) {
	tmpDir := t.TempDir()
	install := filepath.Join(tmpDir, "rti_connext_dds-7.6.0")
	if err := os.MkdirAll(filepath.Join(install, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "bin", "rtiddsspy"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ValidateConnextInstall(install)
	if err == nil || !strings.Contains(err.Error(), "requires Connext Pro 7.7.0 or newer") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunSpyWritesRuntimeLogAndStatistics(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".connext", "spy", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app_1.xml"), []byte(`
<dds><qos_library name="app_1_qos_lib"><qos_profile name="app_1_qos_profile" is_default_qos="true"/></qos_library></dds>`), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	binDir := filepath.Join(install, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(binDir, "rtiddsspy")
	script := `#!/bin/sh
echo "rtiddsspy is listening for data, press CTRL+C to stop it."
echo "2026-05-05 14:55:51.771722 New writer        from 10.0.0.114     : topic=\"Square\" type=\"ShapeType\""
echo "2026-05-08 00:06:58.475269 New data          from 10.0.0.114     : topic=\"Square\" type=\"ShapeType\" sample={\"color\":\"BLUE\"}"
echo "---- Statistics ----"
echo "Discovered 1 DataWriters and 0 DataReaders"
printf "\t1, 0, 0 \t(Topic=\"Square\"  Type=\"ShapeType\")\n"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	rc, err := app.Run(map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}}, ConnextInstall{Path: install, Version: "7.7.0"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rc != 0 {
		t.Fatalf("unexpected rc: %d", rc)
	}
	if !common.FileExists(filepath.Join(tmpDir, ".connext", "spy", "runtime.json")) {
		t.Fatal("runtime.json not written")
	}
	logContent := readFile(t, filepath.Join(tmpDir, ".connext", "spy", "logs", "spy.log"))
	logLines := strings.Split(logContent, "\n")
	if !strings.HasPrefix(logLines[0], "Running ") ||
		!strings.Contains(logLines[0], filepath.Join(install, "bin", "rtiddsspy")) ||
		!strings.Contains(logLines[0], "-domainId 100") ||
		!strings.Contains(logLines[0], "-qosFile "+filepath.Join(tmpDir, ".connext", "spy", "app", "app_1.xml")) ||
		!strings.Contains(logLines[0], "-qosProfile app_1_qos_lib::app_1_qos_profile") {
		t.Fatalf("unexpected first spy log line: %q", logLines[0])
	}
	if strings.Contains(logContent, "New data") || strings.Contains(logContent, "sample=") || strings.Contains(logContent, `{"color":"BLUE"}`) {
		t.Fatalf("spy log should not persist raw sample payloads: %s", logContent)
	}
	if !strings.Contains(logContent, "New writer") || !strings.Contains(logContent, "Discovered 1 DataWriters") {
		t.Fatalf("spy log missing operational lines: %s", logContent)
	}
	output := tui.StripANSIEscapes(out.String())
	if !strings.Contains(output, "Connext Cloud Spy") || !strings.Contains(output, "Square") || !strings.Contains(output, "Discovered 1 DataWriters") {
		t.Fatalf("unexpected output: %s", output)
	}
	if !strings.Contains(output, "• Logs saved under "+filepath.Join(tmpDir, ".connext", "spy", "logs")) {
		t.Fatalf("missing logs hint: %s", output)
	}
	if !strings.Contains(output, "• Run 'rticloud spy' from this directory to start this spy again.") {
		t.Fatalf("missing restart hint: %s", output)
	}
}

func TestRunSpyWithTextOutputPrintsPlainEvents(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".connext", "spy", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app_1.xml"), []byte(`
<dds><qos_library name="app_1_qos_lib"><qos_profile name="app_1_qos_profile" is_default_qos="true"/></qos_library></dds>`), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	binDir := filepath.Join(install, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "2026-05-05 14:55:51.771722 New writer        from 10.0.0.114     : topic=\"Square\" type=\"ShapeType\""
echo "2026-05-08 00:06:58.475269 New data          from 10.0.0.114     : topic=\"Square\" type=\"ShapeType\" sample={\"color\":\"BLUE\"}"
`
	if err := os.WriteFile(filepath.Join(binDir, "rtiddsspy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	rc, err := app.RunWithOptions(map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}}, ConnextInstall{Path: install, Version: "7.7.0"}, true, RunOptions{TextOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if rc != 0 {
		t.Fatalf("unexpected rc: %d", rc)
	}
	output := out.String()
	if !strings.Contains(output, "[writer] Square ShapeType from 10.0.0.114") || !strings.Contains(output, `[data] Square ShapeType {"color":"BLUE"}`) {
		t.Fatalf("unexpected output: %s", output)
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("text output contains ANSI escapes: %q", output)
	}
}

func TestRenderANSIUsesGatewayPanelColorThemes(t *testing.T) {
	view := RenderedView{
		Title:  SpyPanelTitle(),
		Header: SummaryLine{Label: "databus", Status: "[green]● receiving samples[/green]", Target: "db / app", Hosts: []string{"rti-persistence-6d58996bf5-76728", "shape-host"}},
		Topics: []RenderedTopic{{
			Activity:   "[green]●[/green]",
			Topic:      "Square",
			Type:       "ShapeType",
			Writers:    1,
			Samples:    3,
			LastSample: `2026-05-08 00:06:58 {"x":1}`,
		}},
		Samples: []RenderedSample{{Time: "2026-05-08 00:06:58.475269", Topic: "Square", Sample: `{"x":1}`}},
	}
	rendered := renderANSI(view)
	checks := []string{
		"\x1b[1;38;5;208m Connext Cloud Spy",
		"\x1b[1;38;5;110mTopics",
		"\x1b[1;38;5;245mSamples",
		"Last sample",
		"rti-persistence-6d58996bf5-76728, shape-host",
		"2026-05-08 00:06:58.475269",
		`{"x":1}`,
	}
	for _, check := range checks {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing color sequence %q in %q", check, rendered)
		}
	}
}

func TestRenderANSIShowsWaitingHostsMessage(t *testing.T) {
	rendered := renderANSI(RenderedView{
		Title:  SpyPanelTitle(),
		Header: SummaryLine{Label: "databus", Status: "[blue]○ not connected[/]", Target: "db / app"},
		Topics: []RenderedTopic{{
			Activity:   "[dim]○[/dim]",
			Topic:      "waiting",
			Type:       "No topics discovered yet",
			LastSample: "-",
		}},
	})
	if strings.Contains(rendered, "HOSTS") || !strings.Contains(rendered, "no participants discovered yet") {
		t.Fatalf("missing waiting hosts message in %q", rendered)
	}
	lines := formatSummaryLines(SummaryLine{Label: "databus", Status: "[blue]○ not connected[/]", Target: "db / app"}, 100)
	if len(lines) != 2 {
		t.Fatalf("expected databus and participants summary lines: %#v", lines)
	}
	expectedPrefix := strings.Repeat(" ", spySummaryLabelWidth+spySummaryStatusWidth+4) + "no participants discovered yet"
	if !strings.HasPrefix(tui.StripANSIEscapes(lines[1]), expectedPrefix) {
		t.Fatalf("participants line is not aligned under target column: %q", tui.StripANSIEscapes(lines[1]))
	}
}

func TestRenderANSISamplesPanelExpandsToRemainingHeight(t *testing.T) {
	view := NewLiveView(map[string]any{"databus": "db", "templates": map[string]any{"app": "app"}})
	for index := 0; index < SpyLiveSampleRows+2; index++ {
		view.HandleLine(fmt.Sprintf(`2026-05-08 00:06:%02d.475269 New data          from 10.0.0.114     : topic="Square" type="ShapeType" sample={"x":%d}`, index, index))
	}
	rendered := tui.StripANSIEscapes(renderANSIForSize(view.Render(0), spyDefaultWidth, 60))

	lines := strings.Split(rendered, "\n")
	samplesTop := -1
	samplesBottom := -1
	for index, line := range lines {
		if strings.Contains(line, "┌─ Samples") {
			samplesTop = index
			continue
		}
		if samplesTop >= 0 && strings.HasPrefix(line, "└") {
			samplesBottom = index
			break
		}
	}
	if samplesTop < 0 || samplesBottom <= samplesTop {
		t.Fatalf("could not locate samples panel in output:\n%s", rendered)
	}

	panelHeight := samplesBottom - samplesTop + 1
	if panelHeight <= SpyLiveSampleRows+2 {
		t.Fatalf("expected samples panel to grow beyond sample row cap, got height=%d", panelHeight)
	}
	if !strings.Contains(rendered, `{"x":0}`) {
		t.Fatalf("expected expanded samples panel to include data beyond old row cap:\n%s", rendered)
	}
}

func TestLiveViewDatabusStatusDistinguishesConnectedHostsFromSamples(t *testing.T) {
	view := NewLiveView(map[string]any{"databus": "db", "templates": map[string]any{"app": "app"}})
	view.State.serviceState = "running"
	noHosts := tui.StripANSIEscapes(renderANSI(view.Render(0)))
	if !strings.Contains(noHosts, "○ not connected") {
		t.Fatalf("expected not connected status, got %q", noHosts)
	}

	view.HandleLine(`2026-05-26 20:24:39.405954 New participant    from 172.31.38.178  : name="Shape Publisher" hostName="shape-host" processId="456"`)
	connected := tui.StripANSIEscapes(renderANSI(view.Render(0)))
	if !strings.Contains(connected, "○ connected") {
		t.Fatalf("expected connected status, got %q", connected)
	}

	view.HandleLine(`2026-05-08 00:06:58.475269 New data          from 10.0.0.114     : topic="Square" type="ShapeType" sample={"color":"BLUE"}`)
	receiving := tui.StripANSIEscapes(renderANSI(view.Render(1)))
	if !strings.Contains(receiving, "◉ receiving samples") {
		t.Fatalf("expected receiving samples status, got %q", receiving)
	}
}

func TestLiveViewDoesNotShowReceivingSamplesWithoutParticipants(t *testing.T) {
	view := NewLiveView(map[string]any{"databus": "db", "templates": map[string]any{"app": "app"}})
	view.State.serviceState = "running"
	view.HandleLine(`2026-05-08 00:06:58.475269 New data          from 10.0.0.114     : topic="Square" type="ShapeType" sample={"color":"BLUE"}`)

	rendered := tui.StripANSIEscapes(renderANSI(view.Render(0)))
	if !strings.Contains(rendered, "○ not connected") || !strings.Contains(rendered, "no participants discovered yet") || strings.Contains(rendered, "receiving samples") {
		t.Fatalf("expected samples without participants to stay not connected: %q", rendered)
	}
}

func TestResetRemovesConfigAndCredentials(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".connext", "spy", "app")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".connext", "spy.yaml"), []byte("databus: db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(appDir, "app_1.xml")
	if err := os.WriteFile(artifactPath, []byte("<dds/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range common.SecureFiles {
		if err := os.WriteFile(filepath.Join(appDir, name), []byte("spy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	app := NewApp(tmpDir, &out)
	if err := app.Reset(); err != nil {
		t.Fatal(err)
	}
	if common.FileExists(filepath.Join(tmpDir, ".connext", "spy.yaml")) || !common.FileExists(artifactPath) {
		t.Fatalf("unexpected files after reset")
	}
	for _, name := range common.SecureFiles {
		if common.FileExists(filepath.Join(appDir, name)) {
			t.Fatalf("spy credential was not removed: %s", name)
		}
	}
	if !strings.Contains(out.String(), "Removed spy credentials") || !strings.Contains(out.String(), "Runtime artifacts were left in") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func stringValue(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return value
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
