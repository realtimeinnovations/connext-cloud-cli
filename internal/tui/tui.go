// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package tui

import (
	"io"
	"os"
	"regexp"
	"strings"

	"golang.org/x/term"
)

const (
	RTIOrange = "#FF9D00"
	RTIBlue   = "#5f819d"
)

var (
	ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	markupReplacer    = strings.NewReplacer(
		"[green]", "",
		"[/green]", "",
		"[yellow]", "",
		"[/yellow]", "",
		"[red]", "",
		"[/red]", "",
		"[dim]", "",
		"[/dim]", "",
		"[bold]", "",
		"[/bold]", "",
		"[blue]", "",
		"[/]", "",
		"["+RTIBlue+"]", "",
	)
)

type PanelTheme struct {
	TitleStyle  func(string) string
	BorderStyle func(string) string
	PaddedBody  bool
}

func MaxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func MinInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func StripMarkup(value string) string {
	if !strings.Contains(value, "[") {
		return value
	}
	return markupReplacer.Replace(value)
}

func StripANSIEscapes(value string) string {
	stripped := StripMarkup(value)
	if !strings.Contains(stripped, "\x1b") {
		return stripped
	}
	return ansiEscapePattern.ReplaceAllString(stripped, "")
}

func DisplayWidth(value string) int {
	return len([]rune(StripANSIEscapes(value)))
}

func TruncateDisplay(value string, width int) string {
	if width <= 0 {
		return ""
	}
	clean := StripANSIEscapes(value)
	runes := []rune(clean)
	if len(runes) <= width {
		return clean
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
}

func PadDisplay(value string, width int) string {
	clean := StripANSIEscapes(value)
	runes := []rune(clean)
	if len(runes) > width {
		return TruncateDisplay(clean, width)
	}
	return clean + strings.Repeat(" ", width-len(runes))
}

func PadStyled(value string, width int) string {
	visible := DisplayWidth(value)
	if visible >= width {
		return value
	}
	return value + strings.Repeat(" ", width-visible)
}

func RenderPanel(title string, body []string, width int, theme PanelTheme) []string {
	if width < 12 {
		width = 12
	}
	lines := []string{panelTopBorder(title, width, theme)}
	if theme.PaddedBody {
		lines = append(lines, panelBodyLine("", width, theme))
	}
	for _, line := range body {
		lines = append(lines, panelBodyLine(line, width, theme))
	}
	if theme.PaddedBody {
		lines = append(lines, panelBodyLine("", width, theme))
	}
	lines = append(lines, panelBottomBorder(width, theme))
	return lines
}

func panelTopBorder(title string, width int, theme PanelTheme) string {
	innerWidth := MaxInt(1, width-2)
	label := TruncateDisplay(title, MaxInt(1, innerWidth-3))
	filler := MaxInt(0, innerWidth-DisplayWidth(label)-3)
	return theme.BorderStyle("╭─ ") + theme.TitleStyle(label) + theme.BorderStyle(" "+strings.Repeat("─", filler)+"╮")
}

func panelBottomBorder(width int, theme PanelTheme) string {
	return theme.BorderStyle("╰" + strings.Repeat("─", MaxInt(1, width-2)) + "╯")
}

func panelBodyLine(content string, width int, theme PanelTheme) string {
	innerWidth := MaxInt(1, width-4)
	if DisplayWidth(content) > innerWidth {
		content = TruncateDisplay(StripANSIEscapes(content), innerWidth)
	}
	return theme.BorderStyle("│ ") + PadStyled(content, innerWidth) + theme.BorderStyle(" │")
}

func TerminalSize(out io.Writer, defaultWidth int, defaultHeight int) (int, int) {
	file, ok := out.(*os.File)
	if !ok {
		return defaultWidth, defaultHeight
	}
	width, height, err := term.GetSize(int(file.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return defaultWidth, defaultHeight
	}
	return width, height
}

func StyleTitle(value string) string {
	return "\x1b[1;38;5;208m" + value + "\x1b[0m"
}

func StyleSection(value string) string {
	return "\x1b[1;38;5;110m" + value + "\x1b[0m"
}

func StyleLink(value string) string {
	return "\x1b[1;4m" + value + "\x1b[0m"
}

func StyleStrong(value string) string {
	return "\x1b[1m" + value + "\x1b[0m"
}

func StyleMutedSection(value string) string {
	return "\x1b[1;38;5;245m" + value + "\x1b[0m"
}

func StyleOrangeBorder(value string) string {
	return "\x1b[38;5;208m" + value + "\x1b[0m"
}

func StyleBlueBorder(value string) string {
	return "\x1b[38;5;110m" + value + "\x1b[0m"
}

func StyleGrayBorder(value string) string {
	return "\x1b[38;5;245m" + value + "\x1b[0m"
}

func Dim(value string) string {
	return "\x1b[2m" + value + "\x1b[0m"
}

func StyleColumnHeader(value string, width int) string {
	label := TruncateDisplay(value, width)
	padding := MaxInt(0, width-DisplayWidth(label))
	return "\x1b[2;4m" + label + "\x1b[0m" + strings.Repeat(" ", padding)
}

func StyleLabel(value string, width int) string {
	return "\x1b[2;38;5;110m" + PadDisplay(value, width) + "\x1b[0m"
}

func StyleTarget(value string, width int) string {
	return "\x1b[1m" + PadDisplay(value, width) + "\x1b[0m"
}

func StyleBold(value string, width int) string {
	return "\x1b[1m" + PadDisplay(value, width) + "\x1b[0m"
}

func StyleInlineWarning(value string) string {
	if value == "secure" {
		return "\x1b[38;2;95;129;157m(• " + value + ")\x1b[0m"
	}
	return "\x1b[33m(⚠ " + value + ")\x1b[0m"
}

func StyleChipWidth(markup string, width int) string {
	content := PadDisplay(StripMarkup(markup), width)
	switch {
	case strings.Contains(markup, "[green]"):
		return "\x1b[32m" + content + "\x1b[0m"
	case strings.Contains(markup, "[yellow]"):
		return "\x1b[33m" + content + "\x1b[0m"
	case strings.Contains(markup, "[red]"):
		return "\x1b[1;31m" + content + "\x1b[0m"
	case strings.Contains(markup, "[dim]"):
		return Dim(content)
	default:
		return "\x1b[36m" + content + "\x1b[0m"
	}
}
