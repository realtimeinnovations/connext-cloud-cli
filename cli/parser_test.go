package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestParserShowsGeneratedHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute(nil, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for no args (root help), got %v", err)
	}
	if !strings.Contains(out.String(), "Connect to Connext Cloud:") ||
		!strings.Contains(out.String(), "Manage Connext Cloud:") ||
		!strings.Contains(out.String(), "Setup:") ||
		!strings.Contains(out.String(), "rticloud [command] [flags]") ||
		!strings.Contains(out.String(), "gateway") ||
		!strings.Contains(out.String(), "databus") ||
		!strings.Contains(out.String(), "--version") {
		t.Fatalf("unexpected root help: %s", out.String())
	}
	if strings.Contains(out.String(), "\n  version") {
		t.Fatalf("version should only be exposed as --version: %s", out.String())
	}

	out.Reset()
	err = Execute([]string{"databus"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for databus (help), got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Databuses") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected databus help: %s", out.String())
	}

	out.Reset()
	err = Execute([]string{"databus", "create", "--help"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for databus create --help, got %v", err)
	}
	if !strings.Contains(out.String(), "--replicas") || !strings.Contains(out.String(), "--observability-service") {
		t.Fatalf("unexpected databus create help: %s", out.String())
	}
}

func TestParserRejectsUnknownResourceWithoutCommand(t *testing.T) {
	err := Execute([]string{"databu"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected unsupported resource error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestParserRejectsVersionCommand(t *testing.T) {
	err := Execute([]string{"version"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected version command to be unsupported")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestParserVersionFlag(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"--version"}, &out, io.Discard, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "rticloud") {
		t.Fatalf("expected version output, got: %s", out.String())
	}
}

func TestParserRejectsInvalidClientKind(t *testing.T) {
	err := Execute([]string{"client", "create", "--name", "db", "--client-name", "app", "--kind", "collector"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected invalid kind error")
	}
}

func TestParserReportsMissingValuesWithoutPanic(t *testing.T) {
	err := Execute([]string{"databus", "query", "--name"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected missing value error")
	}
}

func TestParserRequiresExactlyOneObservabilityLinkAction(t *testing.T) {
	err := Execute([]string{"databus", "set-observability", "--name", "db"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected missing link action error")
	}
	err = Execute([]string{"databus", "set-observability", "--name", "db", "--service", "obs", "--unlink"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected mutually exclusive link action error")
	}
}

func TestParserRejectsInvalidLiveFormat(t *testing.T) {
	err := Execute([]string{"spy", "--format", "json"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected invalid format error")
	}
	err = Execute([]string{"gateway", "--format", "json"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected invalid format error for gateway")
	}
}

func TestParserRejectsSpyObsCommand(t *testing.T) {
	err := Execute([]string{"spy", "obs"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected unsupported spy command error")
	}
}

func TestParserEdgeProvisioningServiceHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provisioning", "service"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provisioning service help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Provisioning Services") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-provisioning service help: %s", out.String())
	}
}

func TestParserEdgeProvisioningProfileHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provisioning", "profile"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provisioning profile help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Participant Profiles") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-provisioning profile help: %s", out.String())
	}
}

func TestParserEdgeProvisioningCampaignHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provisioning", "campaign"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provisioning campaign help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Campaigns") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-provisioning campaign help: %s", out.String())
	}
}

func TestParserEdgeProvisioningDeviceHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provisioning", "device"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provisioning device help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Devices") || !strings.Contains(out.String(), "list") {
		t.Fatalf("unexpected edge-provisioning device help: %s", out.String())
	}
}

func TestParserEdgeSyncHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-sync"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-sync help, got %v", err)
	}
	if !strings.Contains(out.String(), "Sync security artifacts") || !strings.Contains(out.String(), "healthz") {
		t.Fatalf("unexpected edge-sync help: %s", out.String())
	}
}

func TestParserEdgeProvisioningServiceRequiresName(t *testing.T) {
	err := Execute([]string{"edge-provisioning", "service", "query"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --name required error")
	}
}

func TestParserEdgeProvisioningProfileRequiresService(t *testing.T) {
	err := Execute([]string{"edge-provisioning", "profile", "list"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --service required error")
	}
}

func TestParserEdgeSyncIdentityRequiresParticipantID(t *testing.T) {
	err := Execute([]string{"edge-sync", "identity"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --participant-id required error")
	}
}

func TestParserEdgeSyncRejectsDisableSSLVerify(t *testing.T) {
	err := Execute([]string{"--disable-ssl-verify", "edge-sync", "healthz", "--url", "http://localhost:8080"}, io.Discard, io.Discard, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be used with edge-sync") {
		t.Fatalf("expected disable-ssl-verify rejection, got: %v", err)
	}
}

func TestParserEdgeSyncHelpHidesDisableSSLVerify(t *testing.T) {
	var out bytes.Buffer
	_ = Execute([]string{"edge-sync"}, &out, &out, nil)
	if strings.Contains(out.String(), "disable-ssl-verify") {
		t.Fatalf("disable-ssl-verify should not appear in edge-sync help: %s", out.String())
	}
}

func TestRootHelpShowsOperatorCommands(t *testing.T) {
	var out bytes.Buffer
	_ = Execute(nil, &out, &out, nil)
	output := out.String()
	if !strings.Contains(output, "edge-provisioning") ||
		!strings.Contains(output, "edge-sync") {
		t.Fatalf("expected operator commands in root help: %s", output)
	}
}
