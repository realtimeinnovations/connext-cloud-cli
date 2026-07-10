package tui

import (
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/diagnostics"
)

func TestRenderDiagnosticsPanelWrapsHintAndShowsCount(t *testing.T) {
	findings := []diagnostics.Finding{{
		Severity: diagnostics.SeverityError,
		Summary:  "A required UDP socket could not be created.",
		Hint:     "You may be running another gateway on this machine. Stop it or configure them to use different ports.",
		Count:    3,
	}}
	rendered := StripANSIEscapes(strings.Join(RenderDiagnosticsPanel(findings, 60), "\n"))
	for _, expected := range []string{"⚠ Needs attention", "A required UDP socket could not be created.", "seen 3 times", "different ports."} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("missing %q in rendered panel:\n%s", expected, rendered)
		}
	}
}

func TestDiagnosticRenderingOmitsEmptyHint(t *testing.T) {
	finding := diagnostics.Finding{
		Severity: diagnostics.SeverityError,
		Summary:  "The current configuration of your RTI product does not include Real-Time WAN Transport.",
		Count:    1,
	}
	rendered := StripANSIEscapes(strings.Join(RenderDiagnosticsPanel([]diagnostics.Finding{finding}, 120), "\n"))
	if strings.Contains(rendered, "[hint]") || strings.Contains(FormatTextFinding(finding), "[hint]") {
		t.Fatalf("empty hint was rendered: %q", rendered)
	}
	if !strings.Contains(RenderDiagnosticSummary([]diagnostics.Finding{finding}), finding.Summary) {
		t.Fatalf("shutdown summary omitted diagnostic")
	}
}

func TestDiagnosticSummaryDoesNotIndentWrappedContent(t *testing.T) {
	finding := diagnostics.Finding{
		Severity: diagnostics.SeverityError,
		Summary:  "Component is unavailable.",
		Hint:     "Run the setup again.",
		Count:    1,
	}
	rendered := RenderDiagnosticSummary([]diagnostics.Finding{finding})
	if !strings.Contains(rendered, "\nComponent is unavailable.\nRun the setup again.\n") {
		t.Fatalf("diagnostic summary content is indented: %q", rendered)
	}
}
