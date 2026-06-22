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
	if strings.Contains(out.String(), "--disable-ssl-verify") {
		t.Fatalf("deprecated disable SSL flag should not be exposed: %s", out.String())
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
	if !strings.Contains(out.String(), "--replicas") || !strings.Contains(out.String(), "--observability-service") || !strings.Contains(out.String(), "--non-secure") {
		t.Fatalf("unexpected databus create help: %s", out.String())
	}

	out.Reset()
	err = Execute([]string{"observability", "create", "--help"}, &out, &out, nil)
	if err != nil {
		t.Fatalf("expected nil for observability create --help, got %v", err)
	}
	if !strings.Contains(out.String(), "--network-name") || !strings.Contains(out.String(), "--non-secure") {
		t.Fatalf("unexpected observability create help: %s", out.String())
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

func TestParserShowsSkipPreflightFlag(t *testing.T) {
	var out bytes.Buffer
	if err := Execute([]string{"gateway", "--help"}, &out, &out, nil); err != nil {
		t.Fatalf("unexpected gateway help error: %v", err)
	}
	if !strings.Contains(out.String(), "--skip-preflight") {
		t.Fatalf("expected gateway help to show skip-preflight: %s", out.String())
	}
	out.Reset()
	if err := Execute([]string{"spy", "--help"}, &out, &out, nil); err != nil {
		t.Fatalf("unexpected spy help error: %v", err)
	}
	if !strings.Contains(out.String(), "--skip-preflight") {
		t.Fatalf("expected spy help to show skip-preflight: %s", out.String())
	}
}

func TestParserRejectsSpyObsCommand(t *testing.T) {
	err := Execute([]string{"spy", "obs"}, io.Discard, io.Discard, nil)
	if err == nil {
		t.Fatal("expected unsupported spy command error")
	}
}
