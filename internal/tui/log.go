// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package tui

import (
	"fmt"
	"strings"
	"time"
)

// LogSeverity selects the glyph and color a TUI log panel uses to render a line.
// Callers classify their own lines (their keyword rules differ); this shared
// rendering ensures a given severity looks identical in every panel.
type LogSeverity int

const (
	// LogInfo is neutral/background activity: dim text with a "·" bullet.
	LogInfo LogSeverity = iota
	// LogGood is a positive lifecycle event: green text with a "•" bullet.
	LogGood
	// LogWarn is a problem needing attention: yellow text with a "!" marker.
	LogWarn
)

const (
	// LogPanelMaxLines caps how many recent log lines a single log panel shows.
	LogPanelMaxLines = 12
	// minVisibleLogLines is the number of log lines a shared section split tries
	// to keep visible before squeezing them for the primary section.
	minVisibleLogLines = 4
)

// LogEntry is one renderable log line. A zero Time suppresses the relative-age
// stamp (e.g. for lines whose arrival time is unknown).
type LogEntry struct {
	Text     string
	Severity LogSeverity
	Time     time.Time
}

// CompactLogEntries collapses runs of identical-Text consecutive entries into a
// single "<text> (xN)" entry, preserving the run's severity and the most recent
// occurrence's Time. Both TUI log panels share this so duplicate suppression
// (and the age shown for a collapsed run) behaves identically.
func CompactLogEntries(entries []LogEntry) []LogEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]LogEntry, 0, len(entries))
	cur := entries[0]
	count := 1
	flush := func() {
		e := cur
		if count > 1 {
			e.Text = fmt.Sprintf("%s (x%d)", e.Text, count)
		}
		out = append(out, e)
	}
	for _, e := range entries[1:] {
		if e.Text == cur.Text {
			count++
			cur.Time = e.Time // age tracks the most recent repeat
			continue
		}
		flush()
		cur = e
		count = 1
	}
	flush()
	return out
}

// CompactLogLines is a string convenience wrapper over CompactLogEntries.
func CompactLogLines(lines []string) []string {
	entries := make([]LogEntry, len(lines))
	for i, line := range lines {
		entries[i] = LogEntry{Text: line}
	}
	compacted := CompactLogEntries(entries)
	out := make([]string, len(compacted))
	for i, e := range compacted {
		out[i] = e.Text
	}
	return out
}

// logSeverityStyle returns the glyph prefix and the color function for sev.
func logSeverityStyle(sev LogSeverity) (string, func(string) string) {
	switch sev {
	case LogWarn:
		return "! ", func(s string) string { return "\x1b[33m" + s + "\x1b[0m" }
	case LogGood:
		return "• ", func(s string) string { return "\x1b[32m" + s + "\x1b[0m" }
	default:
		return "· ", Dim
	}
}

// FormatLogEntry trims and truncates the entry to fit contentWidth, prepends the
// severity glyph + color, and — when the entry carries a Time and now is set —
// right-aligns a compact relative age (e.g. "12s", "3m") on the far edge so
// operators can correlate events without opening the log file.
func FormatLogEntry(e LogEntry, contentWidth int, now time.Time) string {
	prefix, colorize := logSeverityStyle(e.Severity)
	trimmed := strings.TrimSpace(e.Text)
	inner := MaxInt(8, contentWidth-2) // width available after the 2-col glyph

	ts := ""
	if !e.Time.IsZero() && !now.IsZero() {
		ts = RelativeAge(now.Sub(e.Time))
	}
	if ts == "" {
		return prefix + colorize(TruncateDisplay(trimmed, inner))
	}
	tsWidth := len([]rune(ts))
	textWidth := MaxInt(4, inner-tsWidth-1) // keep at least a 1-space gap
	text := TruncateDisplay(trimmed, textWidth)
	gap := inner - DisplayWidth(text) - tsWidth
	if gap < 1 {
		gap = 1
	}
	return prefix + colorize(text) + strings.Repeat(" ", gap) + Dim(ts)
}

// FormatLogLine renders a line with a known severity and no timestamp. Retained
// for callers that hold plain strings (the keyword-classified panels).
func FormatLogLine(line string, contentWidth int, sev LogSeverity) string {
	return FormatLogEntry(LogEntry{Text: line, Severity: sev}, contentWidth, time.Time{})
}

// RelativeAge formats a duration as a compact, single-unit age: seconds under a
// minute, then minutes, hours, days.
func RelativeAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// ClampLogBudget returns how many log lines a single log panel should render
// given the body rows available and the number of lines present (total).
// maxLines <= 0 means no cap. The result is clamped to [0, min(total, max,
// available)], with at least one line whenever any space exists.
func ClampLogBudget(available, total, maxLines int) int {
	if available < 1 || total < 1 {
		return 0
	}
	budget := available
	if maxLines > 0 && budget > maxLines {
		budget = maxLines
	}
	if budget > total {
		budget = total
	}
	if budget < 1 {
		budget = 1
	}
	return budget
}

// SplitSectionBudget shares `available` body rows between a primary section
// (e.g. the gateway routes table) and a log section, preferring to keep up to
// minVisibleLogLines of the log visible before squeezing it. Factored here so
// the gateway and any other two-section TUI honor one min-visible policy and
// react identically to small terminals.
func SplitSectionBudget(available, primaryLines, logLines int) (int, int) {
	if available <= 0 {
		return 0, 0
	}
	if primaryLines == 0 {
		primaryLines = 1
	}
	if logLines == 0 {
		logLines = 1
	}
	minLog := MinInt(logLines, minVisibleLogLines)
	primaryBudget := MinInt(primaryLines, MaxInt(1, available-minLog))
	logBudget := MinInt(logLines, MaxInt(1, available-primaryBudget))
	for primaryBudget+logBudget > available && logBudget > 1 {
		logBudget--
	}
	for primaryBudget+logBudget > available && primaryBudget > 1 {
		primaryBudget--
	}
	if primaryBudget+logBudget > available {
		logBudget = MaxInt(0, available-primaryBudget)
	}
	return primaryBudget, logBudget
}
