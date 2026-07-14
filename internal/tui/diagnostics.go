package tui

import (
	"fmt"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/diagnostics"
)

func RenderDiagnosticsPanel(findings []diagnostics.Finding, width int) []string {
	if len(findings) == 0 {
		return nil
	}
	contentWidth := MaxInt(8, width-4)
	body := make([]string, 0, len(findings)*2)
	for index, finding := range findings {
		if index > 0 {
			body = append(body, "")
		}
		summary := finding.Summary
		countLine := ""
		if finding.Count > 1 {
			count := fmt.Sprintf("seen %d times", finding.Count)
			if DisplayWidth(summary)+DisplayWidth(count)+3 <= contentWidth {
				summary += " (" + count + ")"
			} else {
				countLine = "  " + count
			}
		}
		for _, line := range wrapDiagnosticText(summary, contentWidth, "") {
			body = append(body, StyleDiagnosticSummary(line))
		}
		if countLine != "" {
			body = append(body, Dim(countLine))
		}
		if strings.TrimSpace(finding.Hint) != "" {
			body = append(body, wrapDiagnosticText(finding.Hint, contentWidth, "  ")...)
		}
	}
	return RenderPanel("⚠ Needs attention", body, width, PanelTheme{
		TitleStyle:  StyleDiagnosticTitle,
		BorderStyle: StyleYellowBorder,
	})
}

func FormatTextFinding(finding diagnostics.Finding) string {
	output := fmt.Sprintf("[%s] %s\n", finding.Severity, finding.Summary)
	if strings.TrimSpace(finding.Hint) != "" {
		output += fmt.Sprintf("[hint] %s\n", finding.Hint)
	}
	return output
}

func RenderDiagnosticSummary(findings []diagnostics.Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var output strings.Builder
	output.WriteString("\n⚠ Needs attention\n")
	for _, finding := range findings {
		output.WriteString(finding.Summary)
		if finding.Count > 1 {
			fmt.Fprintf(&output, " (seen %d times)", finding.Count)
		}
		output.WriteString("\n")
		if strings.TrimSpace(finding.Hint) != "" {
			fmt.Fprintf(&output, "%s\n", finding.Hint)
		}
	}
	return output.String()
}

func StyleDiagnosticTitle(value string) string {
	return "\x1b[1;33m" + value + "\x1b[0m"
}

func StyleDiagnosticSummary(value string) string {
	return "\x1b[33m" + value + "\x1b[0m"
}

func StyleYellowBorder(value string) string {
	return "\x1b[33m" + value + "\x1b[0m"
}

func wrapDiagnosticText(value string, width int, prefix string) []string {
	available := MaxInt(1, width-DisplayWidth(prefix))
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{prefix}
	}
	lines := []string{}
	current := ""
	for _, word := range words {
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if DisplayWidth(candidate) <= available {
			current = candidate
			continue
		}
		if current != "" {
			lines = append(lines, prefix+current)
		}
		current = word
	}
	if current != "" {
		lines = append(lines, prefix+current)
	}
	return lines
}
