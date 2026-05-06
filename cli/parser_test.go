package cli

import "testing"

func TestParserShowsHelpForNoArgsAndTopLevelHelp(t *testing.T) {
	args, err := ParseArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !args.Help {
		t.Fatalf("expected help for no args: %#v", args)
	}

	args, err = ParseArgs([]string{"-h"})
	if err != nil {
		t.Fatal(err)
	}
	if !args.Help {
		t.Fatalf("expected help for -h: %#v", args)
	}
}

func TestParserShowsHelpForKnownResourceWithoutCommand(t *testing.T) {
	args, err := ParseArgs([]string{"databus"})
	if err != nil {
		t.Fatal(err)
	}
	if !args.Help || args.Resource != "databus" {
		t.Fatalf("expected help for databus resource: %#v", args)
	}
}

func TestParserRejectsUnknownResourceWithoutCommand(t *testing.T) {
	_, err := ParseArgs([]string{"databu"})
	if err == nil {
		t.Fatal("expected unsupported resource error")
	}
	if got := err.Error(); got != "unsupported resource: databu" {
		t.Fatalf("unexpected error: %s", got)
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

func TestParserRegistersLicenseGet(t *testing.T) {
	args, err := ParseArgs([]string{"license", "get", "--expiration-days", "30", "-o", "license.dat"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "license" || args.Command != "get" || !args.HasExpirationDays || args.ExpirationDays != 30 || args.Output != "license.dat" {
		t.Fatalf("unexpected args: %#v", args)
	}
}

func TestParserRegistersVersion(t *testing.T) {
	args, err := ParseArgs([]string{"version"})
	if err != nil {
		t.Fatal(err)
	}
	if args.Resource != "version" {
		t.Fatalf("unexpected args: %#v", args)
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
