package gateway

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
)

func TestDefaultSelectFallsBackToNumberedPromptWhenNonInteractive(t *testing.T) {
	var out bytes.Buffer
	app := NewGatewayApp(t.TempDir(), &out)
	app.In = strings.NewReader("2\n")

	selected, err := app.choose("Select gateway capability:", []string{"data", "observability"})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "observability" {
		t.Fatalf("unexpected selection: %s", selected)
	}
	if !strings.Contains(out.String(), "Select gateway capability:") || !strings.Contains(out.String(), "2. observability") {
		t.Fatalf("unexpected prompt output: %s", out.String())
	}
}

func TestDefaultConfirmReloadUsesActionSelection(t *testing.T) {
	var out bytes.Buffer
	app := NewGatewayApp(t.TempDir(), &out)
	app.In = strings.NewReader("1\n")

	ok, err := app.confirmReload("Reload templates?")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("expected reload action to be selected")
	}
	if !strings.Contains(out.String(), "Reload templates?") || !strings.Contains(out.String(), ReloadTemplateListLabel) || !strings.Contains(out.String(), CancelGatewaySetupLabel) {
		t.Fatalf("unexpected prompt output: %s", out.String())
	}
}

func TestDownloadArtifactsWritesGatewayAndCollectorXML(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	app.APIGet = func(path string) (map[string]any, error) {
		switch {
		case strings.HasSuffix(path, "/applications/gw"):
			return map[string]any{"client_config": "<routing/>"}, nil
		case strings.HasSuffix(path, "/applications/collector"):
			return map[string]any{"client_config": "<collector/>"}, nil
		default:
			return nil, GatewayError{Message: path}
		}
	}
	config := map[string]any{"databus": "inventory", "observability": "obs", "templates": map[string]any{"gateway": "gw", "collector": "collector"}}
	if err := app.DownloadArtifacts(config, true); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(tmpDir, ".connext", "gateway", "routing", "gw.xml")); got != "<routing/>" {
		t.Fatalf("unexpected gateway xml: %s", got)
	}
	if got := readFile(t, filepath.Join(tmpDir, ".connext", "gateway", "collector", "collector.xml")); got != "<collector/>" {
		t.Fatalf("unexpected collector xml: %s", got)
	}
	if !strings.Contains(out.String(), "Downloaded gateway template") || !strings.Contains(out.String(), "Downloaded collector template") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestTemplateSelectionCanCreateNewAndReload(t *testing.T) {
	var out bytes.Buffer
	app := NewGatewayApp(t.TempDir(), &out)
	app.CurrentZoneFunc = func() string { return "dev-cloud" }
	app.ConfirmReloadFunc = func(message string) (bool, error) { return true, nil }
	selection := []string{CreateNewTemplate, "gw-new"}
	app.SelectFunc = func(message string, choices []string) (string, error) {
		selected := selection[0]
		selection = selection[1:]
		return selected, nil
	}
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": "inventory", "clients": map[string]any{"gw": map[string]any{"kind": "gateway"}, "gw-new": map[string]any{"kind": "gateway"}}}, nil
	}
	selected, err := app.selectTemplateOrCreate("inventory", "Databus", "gateway", "Select Gateway template from inventory:", []TemplateItem{{Name: "gw", Kind: "gateway"}})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "gw-new" {
		t.Fatalf("unexpected selection: %s", selected)
	}
	if !strings.Contains(out.String(), "• Create gateway template in Connext Cloud dashboard:") || !strings.Contains(out.String(), DashboardURL("dev-cloud", "inventory", "databus")) {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestFirstRunCanConfigureDataOnly(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	calls := []string{}
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		calls = append(calls, "list")
		return map[string]map[string]any{"inventory": {}}, map[string]map[string]any{"inventory-obs": {}}, nil
	}
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": "inventory", "clients": map[string]any{"gw": map[string]any{"kind": "gateway"}}}, nil
	}
	app.DiscoverConnextInstallFn = func(prompt bool) (ConnextInstall, error) {
		calls = append(calls, "discover")
		return ConnextInstall{Path: install, Version: "7.7.0"}, nil
	}
	app.DownloadArtifactsFunc = func(config map[string]any, force bool) error { return nil }
	app.SelectFunc = func(message string, choices []string) (string, error) {
		calls = append(calls, "select:"+message)
		switch message {
		case "Select gateway capability:":
			return "Data only", nil
		case "Select Databus:":
			return "inventory", nil
		case "Select Gateway template from inventory:":
			return "gw", nil
		default:
			return "", GatewayError{Message: message}
		}
	}
	config, err := app.ConfigureFirstRun(true)
	if err != nil {
		t.Fatal(err)
	}
	if common.StringValue(config, "databus") != "inventory" || config["observability"] != nil || common.NestedString(config, "templates", "gateway") != "gw" || config["runtime"].(map[string]any)["connext_home"] != install {
		t.Fatalf("unexpected config: %#v", config)
	}
	if !strings.Contains(out.String(), "Connext Cloud Gateway setup") || !strings.Contains(out.String(), "Databuses available: 1") {
		t.Fatalf("missing setup intro: %s", out.String())
	}
	if !strings.Contains(out.String(), "Using Connext Pro 7.7.0 at "+install) || !strings.Contains(out.String(), "Configuration saved to "+filepath.Join(tmpDir, ".connext", "gateway.yaml")) {
		t.Fatalf("missing setup status output: %s", out.String())
	}
	if strings.Index(out.String(), "Connext Cloud Gateway setup") > strings.Index(out.String(), "Using Connext Pro 7.7.0 at "+install) {
		t.Fatalf("expected setup intro before Connext selection: %s", out.String())
	}
	if discoverIndex(calls, "discover") > discoverIndex(calls, "select:Select gateway capability:") {
		t.Fatalf("expected Connext discovery before capability selection: %#v", calls)
	}
}

func TestFirstRunFailsBeforeDatabusSelectionWhenConnextMissing(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	steps := []string{}
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		return map[string]map[string]any{"inventory": {}}, map[string]map[string]any{"inventory-obs": {}}, nil
	}
	app.DiscoverConnextInstallFn = func(prompt bool) (ConnextInstall, error) {
		steps = append(steps, "discover")
		return ConnextInstall{}, GatewayError{Message: "Connext Pro 7.3.0 or newer with rtiroutingservice was not found."}
	}
	app.SelectFunc = func(message string, choices []string) (string, error) {
		steps = append(steps, "select:"+message)
		switch message {
		case "Select gateway capability:":
			return "Data only", nil
		case "Select Databus:":
			return "inventory", nil
		default:
			return "", GatewayError{Message: message}
		}
	}
	_, err := app.ConfigureFirstRun(false)
	if err == nil || !strings.Contains(err.Error(), "Connext Pro 7.3.0 or newer with rtiroutingservice was not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	if discoverIndex(steps, "select:Select gateway capability:") < len(steps)+1 {
		t.Fatalf("expected failure before capability selection: %#v", steps)
	}
	if discoverIndex(steps, "select:Select Databus:") < len(steps)+1 {
		t.Fatalf("expected failure before databus selection: %#v", steps)
	}
}

func TestFirstRunCanConfigureObservabilityOnly(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		return map[string]map[string]any{"inventory": {}}, map[string]map[string]any{"inventory-obs": {}}, nil
	}
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": "inventory-obs", "clients": map[string]any{"collector": map[string]any{"kind": "telemetry-service-collector"}}}, nil
	}
	app.DiscoverConnextInstallFn = func(prompt bool) (ConnextInstall, error) {
		return ConnextInstall{Path: filepath.Join(tmpDir, "rti_connext_dds-7.7.0"), Version: "7.7.0"}, nil
	}
	app.DownloadArtifactsFunc = func(config map[string]any, force bool) error { return nil }
	app.SelectFunc = func(message string, choices []string) (string, error) {
		switch message {
		case "Select gateway capability:":
			return "Observability only", nil
		case "Select Observability Service:":
			return "inventory-obs", nil
		case "Select Collector template from inventory-obs:":
			return "collector", nil
		default:
			return "", GatewayError{Message: message}
		}
	}
	config, err := app.ConfigureFirstRun(true)
	if err != nil {
		t.Fatal(err)
	}
	if config["databus"] != nil || common.StringValue(config, "observability") != "inventory-obs" || common.NestedString(config, "templates", "collector") != "collector" {
		t.Fatalf("unexpected config: %#v", config)
	}
	if _, ok := config["runtime"].(map[string]any)["connext_home"]; ok {
		t.Fatalf("unexpected connext_home in config: %#v", config)
	}
}

func TestFirstRunCanCreateGatewayTemplateWhenNoneExist(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	selection := []string{CreateNewTemplate, "gw"}
	resourceCalls := 0
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		return map[string]map[string]any{"inventory": {}}, map[string]map[string]any{}, nil
	}
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		resourceCalls++
		if resourceCalls == 1 {
			return map[string]any{"name": "inventory", "clients": map[string]any{}}, nil
		}
		return map[string]any{"name": "inventory", "clients": map[string]any{"gw": map[string]any{"kind": "gateway"}}}, nil
	}
	app.DiscoverConnextInstallFn = func(prompt bool) (ConnextInstall, error) { return ConnextInstall{Path: install, Version: "7.7.0"}, nil }
	app.DownloadArtifactsFunc = func(config map[string]any, force bool) error { return nil }
	app.ConfirmReloadFunc = func(message string) (bool, error) {
		if message != "Reload template list after creating it in the dashboard." {
			return false, GatewayError{Message: message}
		}
		return true, nil
	}
	app.SelectFunc = func(message string, choices []string) (string, error) {
		switch message {
		case "Select gateway capability:":
			return "Data only", nil
		case "Select Databus:":
			return "inventory", nil
		case "Select Gateway template from inventory:":
			selected := selection[0]
			selection = selection[1:]
			return selected, nil
		default:
			return "", GatewayError{Message: message}
		}
	}
	config, err := app.ConfigureFirstRun(true)
	if err != nil {
		t.Fatal(err)
	}
	if common.NestedString(config, "templates", "gateway") != "gw" {
		t.Fatalf("unexpected config: %#v", config)
	}
	if !strings.Contains(out.String(), "• Create gateway template in Connext Cloud dashboard:") || !strings.Contains(out.String(), "Reloading templates...") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestFirstRunCanCreateCollectorTemplateWhenNoneExist(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		return map[string]map[string]any{}, map[string]map[string]any{"inventory-obs": {}}, nil
	}
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": "inventory-obs", "clients": map[string]any{}}, nil
	}
	app.DownloadArtifactsFunc = func(config map[string]any, force bool) error { return nil }
	app.CreateApplicationFunc = func(databusName string, kind string, clientName string) error {
		if databusName != "inventory-obs" || kind != "telemetry-service-collector" || clientName != "collector" {
			return GatewayError{Message: fmt.Sprintf("unexpected args: %s %s %s", databusName, kind, clientName)}
		}
		return nil
	}
	app.SelectFunc = func(message string, choices []string) (string, error) {
		switch message {
		case "Select gateway capability:":
			return "Observability only", nil
		case "Select Observability Service:":
			return "inventory-obs", nil
		case "Select Collector template from inventory-obs:":
			return "Create a new one...", nil
		default:
			return "", GatewayError{Message: message}
		}
	}
	app.InputFunc = func(message string) (string, error) {
		if message != "Collector name" {
			return "", GatewayError{Message: message}
		}
		return "collector", nil
	}
	config, err := app.ConfigureFirstRun(true)
	if err != nil {
		t.Fatal(err)
	}
	if common.NestedString(config, "templates", "collector") != "collector" {
		t.Fatalf("unexpected config: %#v", config)
	}
}

func TestFirstRunAnnotatesLinkedObservabilityChoice(t *testing.T) {
	tmpDir := t.TempDir()
	app := NewGatewayApp(tmpDir, &bytes.Buffer{})
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	app.ListResourcesFunc = func() (map[string]map[string]any, map[string]map[string]any, error) {
		return map[string]map[string]any{"inventory": {}}, map[string]map[string]any{"inventory-obs": {}, "other-obs": {}}, nil
	}
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		switch name {
		case "inventory":
			return map[string]any{"name": "inventory", "config": map[string]any{"observability_service": "inventory-obs"}, "clients": map[string]any{"gw": map[string]any{"kind": "gateway"}}}, nil
		case "inventory-obs":
			return map[string]any{"name": "inventory-obs", "clients": map[string]any{"inventory_gw": map[string]any{"kind": "telemetry-service-collector"}}}, nil
		default:
			return nil, GatewayError{Message: name}
		}
	}
	app.DiscoverConnextInstallFn = func(prompt bool) (ConnextInstall, error) { return ConnextInstall{Path: install, Version: "7.7.0"}, nil }
	app.DownloadArtifactsFunc = func(config map[string]any, force bool) error { return nil }
	app.SelectFunc = func(message string, choices []string) (string, error) {
		switch message {
		case "Select gateway capability:":
			return "Data and Observability", nil
		case "Select Databus:":
			return "inventory", nil
		case "Select Gateway template from inventory:":
			return "gw", nil
		case "Select Observability Service:":
			if selectionLabel(choices[0]) != "inventory-obs  (linked to inventory)" {
				return "", GatewayError{Message: selectionLabel(choices[0])}
			}
			return selectionValue(choices[0]), nil
		default:
			return "", GatewayError{Message: message}
		}
	}
	config, err := app.ConfigureFirstRun(true)
	if err != nil {
		t.Fatal(err)
	}
	if common.StringValue(config, "observability") != "inventory-obs" {
		t.Fatalf("unexpected config: %#v", config)
	}
	if common.NestedString(config, "templates", "collector") != "inventory_gw" {
		t.Fatalf("expected collector template inventory_gw, got: %#v", config)
	}
}

func discoverIndex(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return len(values) + 1
}

func TestValidateConfigResourcesPointsToObservabilityDashboard(t *testing.T) {
	app := NewGatewayApp(t.TempDir(), &bytes.Buffer{})
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		if name == "inventory" {
			return map[string]any{"name": "inventory", "clients": map[string]any{"gw": map[string]any{"kind": "gateway"}}}, nil
		}
		return map[string]any{"name": "inventory-obs", "clients": map[string]any{}}, nil
	}
	config := map[string]any{"zone": "dev-local", "databus": "inventory", "observability": "inventory-obs", "templates": map[string]any{"gateway": "gw", "collector": "collector"}}
	err := app.ValidateConfigResources(config)
	if err == nil || !strings.Contains(err.Error(), "Collector template 'collector' was not found") || !strings.Contains(err.Error(), DashboardURL("dev-local", "inventory-obs", "observability")) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSecureCollectorCredentialsSavedUnderSecureSubdirectory(t *testing.T) {
	tmpDir := t.TempDir()
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"name": name, "config": map[string]any{"secure": name == "obs"}}, nil
	}
	app.GenerateCSRFunc = func(resource string, template string, clientID string) ([]byte, string, error) {
		return []byte("private-key"), "csr", nil
	}
	app.APIPost = func(path string, payload map[string]any) (map[string]any, error) {
		return map[string]any{"secure_files": map[string]any{
			"client.crt":             base64.StdEncoding.EncodeToString([]byte("cert")),
			"identity_ca.crt":        base64.StdEncoding.EncodeToString([]byte("identity")),
			"permissions_ca.crt":     base64.StdEncoding.EncodeToString([]byte("permissions")),
			"signed_governance.p7s":  base64.StdEncoding.EncodeToString([]byte("governance")),
			"signed_permissions.p7s": base64.StdEncoding.EncodeToString([]byte("signed")),
			"psk.key":                base64.StdEncoding.EncodeToString([]byte("psk")),
		}}, nil
	}
	config := map[string]any{"databus": "db", "observability": "obs", "templates": map[string]any{"gateway": "gw", "collector": "collector"}, "clients": map[string]any{"collector_client_id": "collector-1"}}
	databusSecure, collectorSecure, err := app.EnsureSecureArtifacts(config)
	if err != nil {
		t.Fatal(err)
	}
	if databusSecure || !collectorSecure {
		t.Fatalf("unexpected secure flags: %v %v", databusSecure, collectorSecure)
	}
	if got := readBytes(t, filepath.Join(tmpDir, ".connext", "gateway", "collector", "secure", "client.key")); string(got) != "private-key" {
		t.Fatalf("unexpected client.key: %s", string(got))
	}
	if info, err := os.Stat(filepath.Join(tmpDir, ".connext", "gateway", "collector", "secure", "client.key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("client.key permissions = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if got := readBytes(t, filepath.Join(tmpDir, ".connext", "gateway", "collector", "secure", "client.crt")); string(got) != "cert" {
		t.Fatalf("unexpected client.crt: %s", string(got))
	}
}

func TestStartCollectorLaunchesProcess(t *testing.T) {
	tmpDir := t.TempDir()
	collectorDir := filepath.Join(tmpDir, ".connext", "gateway", "collector")
	if err := os.MkdirAll(collectorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collectorDir, "coll1.xml"), []byte("<collector/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	binDir := filepath.Join(install, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(binDir, "rticollectorservicelite")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\ntrap '' INT TERM\nwhile true; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	config := map[string]any{"observability": "obs", "templates": map[string]any{"collector": "coll1"}}
	cmd, err := app.StartCollector(config, ConnextInstall{Path: install, Version: "7.7.0"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid == 0 {
		t.Fatal("expected a running process")
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if !strings.Contains(out.String(), "Collector Service started") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if !common.FileExists(filepath.Join(tmpDir, ".connext", "gateway", "logs", CollectorLogName)) {
		t.Fatalf("collector log not created")
	}
	logContent := readFile(t, filepath.Join(tmpDir, ".connext", "gateway", "logs", CollectorLogName))
	logLines := strings.Split(logContent, "\n")
	if !strings.HasPrefix(logLines[0], "Running ") ||
		!strings.Contains(logLines[0], filepath.Join(install, "bin", "rticollectorservicelite")) ||
		!strings.Contains(logLines[0], "-cfgFile "+filepath.Join(tmpDir, ".connext", "gateway", "collector", "coll1.xml")) ||
		!strings.Contains(logLines[0], "-cfgName coll1") {
		t.Fatalf("unexpected first collector log line: %q", logLines[0])
	}
}

func TestRunCollectorServiceWritesCommandLineToLog(t *testing.T) {
	tmpDir := t.TempDir()
	collectorDir := filepath.Join(tmpDir, ".connext", "gateway", "collector")
	if err := os.MkdirAll(collectorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collectorDir, "coll1.xml"), []byte("<collector/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	binDir := filepath.Join(install, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(binDir, "rticollectorservicelite")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho ready\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	rc, err := app.RunCollectorService(map[string]any{"observability": "obs", "templates": map[string]any{"collector": "coll1"}}, ConnextInstall{Path: install, Version: "7.7.0"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if rc != 0 {
		t.Fatalf("unexpected rc: %d", rc)
	}
	logContent := readFile(t, filepath.Join(tmpDir, ".connext", "gateway", "logs", CollectorLogName))
	logLines := strings.Split(logContent, "\n")
	if !strings.HasPrefix(logLines[0], "Running ") ||
		!strings.Contains(logLines[0], filepath.Join(install, "bin", "rticollectorservicelite")) ||
		!strings.Contains(logLines[0], "-cfgFile "+filepath.Join(tmpDir, ".connext", "gateway", "collector", "coll1.xml")) ||
		!strings.Contains(logLines[0], "-cfgName coll1") {
		t.Fatalf("unexpected first collector log line: %q", logLines[0])
	}
	if !strings.Contains(out.String(), "• Logs saved under "+filepath.Join(tmpDir, ".connext", "gateway", "logs")) {
		t.Fatalf("missing logs hint: %s", out.String())
	}
	if !strings.Contains(out.String(), "• Run 'rticloud gateway' from this directory to start this gateway again.") {
		t.Fatalf("missing restart hint: %s", out.String())
	}
}

func TestRunRoutingServiceWritesRuntimeStateAndLogs(t *testing.T) {
	t.Setenv("RTI_MONITORING2_ENABLE", "true")
	tmpDir := t.TempDir()
	routingDir := filepath.Join(tmpDir, ".connext", "gateway", "routing")
	if err := os.MkdirAll(routingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routingDir, "gw.xml"), []byte("<routing/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	binDir := filepath.Join(install, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(binDir, "rtiroutingservice")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho ready\necho monitoring=$RTI_MONITORING2_ENABLE\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	rc, err := app.RunRoutingService(map[string]any{"databus": "db", "templates": map[string]any{"gateway": "gw"}}, ConnextInstall{Path: install, Version: "7.7.0"}, 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if rc != 0 {
		t.Fatalf("unexpected rc: %d", rc)
	}
	if !common.FileExists(filepath.Join(tmpDir, ".connext", "gateway", "runtime.json")) {
		t.Fatalf("runtime.json not written")
	}
	logContent := readFile(t, filepath.Join(tmpDir, ".connext", "gateway", "logs", "routing.log"))
	logLines := strings.Split(logContent, "\n")
	if !strings.HasPrefix(logLines[0], "Running ") ||
		!strings.Contains(logLines[0], filepath.Join(install, "bin", "rtiroutingservice")) ||
		!strings.Contains(logLines[0], "-cfgFile "+filepath.Join(tmpDir, ".connext", "gateway", "routing", "gw.xml")) ||
		!strings.Contains(logLines[0], "-cfgName gw_gateway") ||
		!strings.Contains(logLines[0], "-verbosity LOCAL:WARN") {
		t.Fatalf("unexpected first routing log line: %q", logLines[0])
	}
	if !strings.Contains(logContent, "ready") {
		t.Fatalf("routing log missing ready")
	}
	if !strings.Contains(logContent, "monitoring=false") {
		t.Fatalf("routing log missing subprocess monitoring env")
	}
	if !strings.Contains(out.String(), "(⚠ not secure)") {
		t.Fatalf("missing dashboard warning: %s", out.String())
	}
	if !strings.Contains(out.String(), "• Logs saved under "+filepath.Join(tmpDir, ".connext", "gateway", "logs")) {
		t.Fatalf("missing logs hint: %s", out.String())
	}
	if !strings.Contains(out.String(), "• Run 'rticloud gateway' from this directory to start this gateway again.") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRunRoutingServiceWithTextOutputPrintsPlainEvents(t *testing.T) {
	tmpDir := t.TempDir()
	routingDir := filepath.Join(tmpDir, ".connext", "gateway", "routing")
	if err := os.MkdirAll(routingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routingDir, "gw.xml"), []byte("<routing/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	binDir := filepath.Join(install, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
echo "RTI Routing Service 7.7.0 executing (with configuration=gw)"
echo "LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED] name=Square, type_name=ShapeType, connection=edge_participant_domain_0"
echo "LOCAL [/routing_services/gateway/domain_routes/etc/sessions/default_session/routes/route_0_all_edge_to_cloud@Square|RUN]"
`
	if err := os.WriteFile(filepath.Join(binDir, "rtiroutingservice"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	rc, err := app.RunRoutingServiceWithOptions(map[string]any{"databus": "db", "templates": map[string]any{"gateway": "gw"}}, ConnextInstall{Path: install, Version: "7.7.0"}, 0, true, true, RunOptions{TextOutput: true})
	if err != nil {
		t.Fatal(err)
	}
	if rc != 0 {
		t.Fatalf("unexpected rc: %d", rc)
	}
	output := out.String()
	if !strings.Contains(output, "[status] running") || !strings.Contains(output, "[route] Square edge_to_cloud run") {
		t.Fatalf("unexpected output: %s", output)
	}
	if strings.Contains(output, "\x1b[") {
		t.Fatalf("text output contains ANSI escapes: %q", output)
	}
}

func TestRunRoutingServiceInterruptRendersStoppedScreen(t *testing.T) {
	tmpDir := t.TempDir()
	routingDir := filepath.Join(tmpDir, ".connext", "gateway", "routing")
	if err := os.MkdirAll(routingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routingDir, "gw.xml"), []byte("<routing/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	binDir := filepath.Join(install, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(binDir, "rtiroutingservice")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\ntrap 'exit 0' INT TERM\nprintf 'ready\\n'\nwhile true; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	interrupts := make(chan os.Signal, 1)
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	app.InterruptSignalFunc = func() (<-chan os.Signal, func()) {
		return interrupts, func() {}
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		interrupts <- os.Interrupt
	}()
	rc, err := app.RunRoutingService(map[string]any{"databus": "db", "templates": map[string]any{"gateway": "gw"}}, ConnextInstall{Path: install, Version: "7.7.0"}, 0, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if rc != 0 && rc != 130 {
		t.Fatalf("unexpected rc: %d", rc)
	}
	if !strings.Contains(out.String(), "• Logs saved under "+filepath.Join(tmpDir, ".connext", "gateway", "logs")) {
		t.Fatalf("missing logs hint: %s", out.String())
	}
	if !strings.Contains(out.String(), "Gateway interrupted.") || !strings.Contains(out.String(), "• Run 'rticloud gateway' from this directory to start this gateway again.") {
		t.Fatalf("unexpected output: %s", out.String())
	}
	if !strings.Contains(out.String(), "stopped") {
		t.Fatalf("final stopped screen missing: %s", out.String())
	}
}

func TestStartRoutingProcessStreamsLineBeforeExit(t *testing.T) {
	if !supportsRoutingPTY() {
		t.Skip("PTY mode is Unix-only")
	}
	cmd := exec.Command("/bin/sh", "-c", "printf 'ready\n'; sleep 1")
	reader, stderr, err := startRoutingProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer closeIfNotNil(reader)
	defer closeIfNotNil(stderr)
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(reader)
		if scanner.Scan() {
			lineCh <- scanner.Text()
			return
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
			return
		}
		errCh <- cmd.Wait()
	}()
	select {
	case line := <-lineCh:
		if line != "ready" {
			t.Fatalf("unexpected line: %s", line)
		}
	case err := <-errCh:
		t.Fatalf("did not receive line-buffered output: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for routing output")
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func TestStatusReportsMissingConfig(t *testing.T) {
	var out bytes.Buffer
	app := NewGatewayApp(t.TempDir(), &out)
	if err := app.Status(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No gateway configuration found in this project.") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestResetRemovesOnlyConfig(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".connext", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".connext", "gateway.yaml"), []byte("databus: db\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(tmpDir, ".connext", "gateway", "artifact.xml")
	if err := os.WriteFile(artifactPath, []byte("<xml/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := NewGatewayApp(tmpDir, &out)
	if err := app.Reset(); err != nil {
		t.Fatal(err)
	}
	if common.FileExists(filepath.Join(tmpDir, ".connext", "gateway.yaml")) || !common.FileExists(artifactPath) {
		t.Fatalf("unexpected files after reset")
	}
	if !strings.Contains(out.String(), "Runtime artifacts were left in") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestOpenObservabilityDashboardOpensGrafanaURL(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".connext"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".connext", "gateway.yaml"), []byte("observability: obs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var opened []string
	app := NewGatewayApp(tmpDir, &out)
	app.GetResourceFunc = func(name string) (map[string]any, error) {
		return map[string]any{"entrypoints": map[string]any{"grafana": map[string]any{"url": "https://grafana.example"}}}, nil
	}
	app.OpenBrowserFunc = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	if err := app.OpenObservabilityDashboard(); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "https://grafana.example" {
		t.Fatalf("unexpected opened urls: %#v", opened)
	}
	if !strings.Contains(out.String(), "Opening Observability dashboard") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
