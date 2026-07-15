// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package tui

import (
	"strings"
	"testing"
)

func TestCompactLogLines(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, nil},
		{"single", []string{"a"}, []string{"a"}},
		{"no runs", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"collapses run", []string{"a", "a", "a", "b"}, []string{"a (x3)", "b"}},
		{"non-adjacent not merged", []string{"a", "b", "a"}, []string{"a", "b", "a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CompactLogLines(c.in)
			if strings.Join(got, "|") != strings.Join(c.want, "|") {
				t.Fatalf("CompactLogLines(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestFormatLogLine_GlyphAndColor(t *testing.T) {
	cases := []struct {
		sev          LogSeverity
		wantPrefix   string
		wantContains string // ANSI color
	}{
		{LogInfo, "· ", "\x1b[2m"},  // dim
		{LogGood, "• ", "\x1b[32m"}, // green
		{LogWarn, "! ", "\x1b[33m"}, // yellow
	}
	for _, c := range cases {
		got := FormatLogLine("a diagnostic line", 80, c.sev)
		if !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("sev %d prefix = %q, want %q", c.sev, got[:2], c.wantPrefix)
		}
		if !strings.Contains(got, c.wantContains) {
			t.Errorf("sev %d = %q, want it to contain %q", c.sev, got, c.wantContains)
		}
	}
}

// TestFormatLogLine_Truncates verifies the line is trimmed and fit to width.
func TestFormatLogLine_Truncates(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := FormatLogLine("   "+long+"   ", 20, LogInfo)
	if w := DisplayWidth(got); w > 20 {
		t.Fatalf("formatted width = %d, want <= 20", w)
	}
	if !strings.HasSuffix(StripANSIEscapes(got), "…") {
		t.Fatalf("expected truncation ellipsis, got %q", StripANSIEscapes(got))
	}
}
