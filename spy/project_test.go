package spy

import (
	"bytes"
	"encoding/base64"
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
		if message != "Reload application list after creating it in the dashboard." {
			return false, UserError{Message: message}
		}
		return true, nil
	}
	app.SelectFunc = func(message string, choices []string) (string, error) {
		switch message {
		case "Select Databus:":
			return "inventory", nil
		case "Select Cloud Native application from inventory:":
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
	if !strings.Contains(out.String(), "Downloaded cloud application template") {
		t.Fatalf("unexpected output: %s", out.String())
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
	if !strings.Contains(readFile(t, filepath.Join(tmpDir, ".connext", "spy", "logs", "spy.log")), "New data") {
		t.Fatal("spy log missing data")
	}
	output := tui.StripANSIEscapes(out.String())
	if !strings.Contains(output, "Connext Cloud Spy") || !strings.Contains(output, "Square") || !strings.Contains(output, "Discovered 1 DataWriters") {
		t.Fatalf("unexpected output: %s", output)
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
		Header: SummaryLine{Label: "databus", Status: "[green]● receiving 1 topic[/green]", Target: "db / app"},
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
		"2026-05-08 00:06:58.475269",
		`{"x":1}`,
	}
	for _, check := range checks {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing color sequence %q in %q", check, rendered)
		}
	}
}

func stringValue(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return value
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
