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

func TestParserEdgeSystemHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-system"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-system help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Edge Systems") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-system help: %s", out.String())
	}
}

func TestParserEdgeParticipantHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-participant"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-participant help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Edge Participant Profiles") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-participant help: %s", out.String())
	}
}

func TestParserEdgeCampaignHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-campaign"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-campaign help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Edge Campaigns") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected edge-campaign help: %s", out.String())
	}
}

func TestParserEdgeDeviceHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-device"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-device help, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Edge Devices") || !strings.Contains(out.String(), "list") {
		t.Fatalf("unexpected edge-device help: %s", out.String())
	}
}

func TestParserEdgeProvisionHelp(t *testing.T) {
	var out bytes.Buffer
	err := Execute([]string{"edge-provision"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for edge-provision help, got %v", err)
	}
	if !strings.Contains(out.String(), "Edge Provision API") || !strings.Contains(out.String(), "healthz") {
		t.Fatalf("unexpected edge-provision help: %s", out.String())
	}
}

func TestParserEdgeSystemRequiresName(t *testing.T) {
	err := Execute([]string{"edge-system", "query"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --name required error")
	}
}

func TestParserEdgeParticipantRequiresEdgeSystem(t *testing.T) {
	err := Execute([]string{"edge-participant", "list"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --edge-system required error")
	}
}

func TestParserEdgeProvisionIdentityRequiresParticipantID(t *testing.T) {
	err := Execute([]string{"edge-provision", "identity"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected --participant-id required error")
	}
}

func TestRootHelpShowsEdgeCommands(t *testing.T) {
	var out bytes.Buffer
	_ = Execute(nil, &out, &out, nil)
	output := out.String()
	if !strings.Contains(output, "edge-system") ||
		!strings.Contains(output, "edge-participant") ||
		!strings.Contains(output, "edge-campaign") ||
		!strings.Contains(output, "edge-device") ||
		!strings.Contains(output, "edge-provision") {
		t.Fatalf("expected edge commands in root help: %s", output)
	}
}
