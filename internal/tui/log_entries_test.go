// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package tui

import (
	"strings"
	"testing"
	"time"
)

func TestRelativeAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m"},
		{45 * time.Minute, "45m"},
		{3 * time.Hour, "3h"},
		{50 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := RelativeAge(c.d); got != c.want {
			t.Errorf("RelativeAge(%s) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestFormatLogEntry_RightAlignedAge(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 30, 0, time.UTC)
	e := LogEntry{Text: "did a thing", Severity: LogGood, Time: now.Add(-12 * time.Second)}
	got := FormatLogEntry(e, 40, now)

	if !strings.HasPrefix(got, "• ") {
		t.Fatalf("prefix = %q, want green bullet", got[:4])
	}
	plain := StripANSIEscapes(got)
	if !strings.HasSuffix(plain, "12s") {
		t.Fatalf("plain = %q, want trailing age 12s", plain)
	}
	if DisplayWidth(got) > 40 {
		t.Fatalf("width = %d, want <= 40", DisplayWidth(got))
	}
}

func TestFormatLogEntry_NoTimeNoStamp(t *testing.T) {
	got := FormatLogEntry(LogEntry{Text: "x", Severity: LogInfo}, 40, time.Now())
	if strings.ContainsAny(StripANSIEscapes(got), "0123456789") {
		t.Fatalf("zero-time entry should have no age stamp, got %q", StripANSIEscapes(got))
	}
}

func TestCompactLogEntries_PreservesSeverityAndLatestTime(t *testing.T) {
	t0 := time.Unix(100, 0)
	t1 := time.Unix(160, 0)
	in := []LogEntry{
		{Text: "x", Severity: LogWarn, Time: t0},
		{Text: "x", Severity: LogWarn, Time: t1},
		{Text: "y", Severity: LogGood, Time: t1},
	}
	out := CompactLogEntries(in)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Text != "x (x2)" || out[0].Severity != LogWarn || !out[0].Time.Equal(t1) {
		t.Fatalf("entry0 = %+v, want x (x2)/LogWarn/latest-time", out[0])
	}
	if out[1].Text != "y" || out[1].Severity != LogGood {
		t.Fatalf("entry1 = %+v, want y/LogGood", out[1])
	}
}

func TestClampLogBudget(t *testing.T) {
	cases := []struct {
		available, total, max, want int
	}{
		{0, 5, 12, 0},   // no room
		{3, 0, 12, 0},   // nothing to show
		{20, 5, 12, 5},  // limited by total
		{20, 30, 12, 12}, // limited by cap
		{8, 30, 12, 8},  // limited by available
		{5, 30, 0, 5},   // no cap
	}
	for _, c := range cases {
		if got := ClampLogBudget(c.available, c.total, c.max); got != c.want {
			t.Errorf("ClampLogBudget(%d,%d,%d) = %d, want %d", c.available, c.total, c.max, got, c.want)
		}
	}
}

func TestSplitSectionBudget(t *testing.T) {
	// Plenty of room: both fit.
	if p, l := SplitSectionBudget(20, 10, 6); p != 10 || l != 6 {
		t.Errorf("ample: got (%d,%d), want (10,6)", p, l)
	}
	// Tight: log keeps up to its min-visible share, primary squeezed.
	p, l := SplitSectionBudget(8, 20, 20)
	if p+l > 8 {
		t.Errorf("over budget: %d+%d > 8", p, l)
	}
	if l < 1 {
		t.Errorf("log should keep at least 1 line, got %d", l)
	}
}
