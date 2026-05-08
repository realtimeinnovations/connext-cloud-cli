package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParserShowsGeneratedHelp(t *testing.T) {
	var out bytes.Buffer
	_, err := ParseArgsWithOutput(nil, &out, &out)
	if !errors.Is(err, ErrHelp) {
		t.Fatalf("expected help for no args, got %v", err)
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
	_, err = ParseArgsWithOutput([]string{"databus"}, &out, &out)
	if !errors.Is(err, ErrHelp) {
		t.Fatalf("expected help for databus, got %v", err)
	}
	if !strings.Contains(out.String(), "Manage Databuses") || !strings.Contains(out.String(), "create") {
		t.Fatalf("unexpected databus help: %s", out.String())
	}

	out.Reset()
	_, err = ParseArgsWithOutput([]string{"databus", "create", "--help"}, &out, &out)
	if !errors.Is(err, ErrHelp) {
		t.Fatalf("expected help for databus create, got %v", err)
	}
	if !strings.Contains(out.String(), "--replicas") || !strings.Contains(out.String(), "--observability-service") {
		t.Fatalf("unexpected databus create help: %s", out.String())
	}
}

func TestParserRejectsUnknownResourceWithoutCommand(t *testing.T) {
	_, err := ParseArgs([]string{"databu"})
	if err == nil {
		t.Fatal("expected unsupported resource error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestParserRegistersGatewayAndObservabilityCollectorKind(t *testing.T) {
	args, err := ParseArgs([]string{"client", "create", "--name", "obs", "--client-name", "collector", "--kind", "observability-collector"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Kind != "observability-collector" {
		t.Fatalf("unexpected kind: %s", args.Kind)
	}
}

func TestParserRegistersGatewayDefaultCommand(t *testing.T) {
	args, err := ParseArgs([]string{"gateway"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "gateway" || args.GatewayCommand != "" {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestParserRegistersLiveTextFormat(t *testing.T) {
	args, err := ParseArgs([]string{"gateway", "--format", "text"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "gateway" || args.GatewayCommand != "" || args.Format != "text" {
		t.Fatalf("unexpected args: %#v", args)
	}

	args, err = ParseArgs([]string{"spy", "status", "--format", "text"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "spy" || args.SpyCommand != "status" || args.Format != "text" {
		t.Fatalf("unexpected args: %#v", args)
	}

	_, err = ParseArgs([]string{"spy", "--format", "json"})
	if err == nil {
		t.Fatal("expected invalid format error")
	}
}

func TestParserRegistersSpyCommands(t *testing.T) {
	args, err := ParseArgs([]string{"spy"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "spy" || args.SpyCommand != "" {
		t.Fatalf("unexpected args: %#v", args)
	}

	args, err = ParseArgs([]string{"spy", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "spy" || args.SpyCommand != "status" {
		t.Fatalf("unexpected args: %#v", args)
	}

	_, err = ParseArgs([]string{"spy", "obs"})
	if err == nil {
		t.Fatal("expected unsupported spy command error")
	}
}

func TestParserRegistersLicenseGet(t *testing.T) {
	args, err := ParseArgs([]string{"license", "get", "--expiration-days", "30", "-o", "license.dat"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "license" || args.Command != "get" || !args.HasExpirationDays || args.ExpirationDays != 30 || args.Output != "license.dat" {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestParserRejectsVersionCommand(t *testing.T) {
	_, err := ParseArgs([]string{"version"})
	if err == nil {
		t.Fatal("expected version command to be unsupported")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("unexpected error: %s", err)
	}
}

func TestParserRegistersVersionFlag(t *testing.T) {
	args, err := ParseArgs([]string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "version" {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestParserRejectsInvalidClientKind(t *testing.T) {
	_, err := ParseArgs([]string{"client", "create", "--name", "db", "--client-name", "app", "--kind", "collector"})
	if err == nil {
		t.Fatal("expected invalid kind error")
	}
}

func TestParserReportsMissingValuesWithoutPanic(t *testing.T) {
	_, err := ParseArgs([]string{"databus", "query", "--name"})
	if err == nil {
		t.Fatal("expected missing value error")
	}
}

func TestParserRequiresExactlyOneObservabilityLinkAction(t *testing.T) {
	_, err := ParseArgs([]string{"databus", "set-observability", "--name", "db"})
	if err == nil {
		t.Fatal("expected missing link action error")
	}
	_, err = ParseArgs([]string{"databus", "set-observability", "--name", "db", "--service", "obs", "--unlink"})
	if err == nil {
		t.Fatal("expected mutually exclusive link action error")
	}
}
