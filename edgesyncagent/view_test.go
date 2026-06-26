// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

// visibleFrameLines strips framePaint's control codes and returns the rendered
// lines with their visible (ANSI-free) width.
func visibleFrameLines(frame string) []string {
	frame = strings.TrimPrefix(frame, "\x1b[?2026h\x1b[H")
	repl := strings.NewReplacer("\x1b[K", "", "\x1b[J", "", "\x1b[?2026l", "")
	raw := strings.Split(frame, "\r\n")
	out := make([]string, 0, len(raw))
	for _, ln := range raw {
		out = append(out, tui.StripANSIEscapes(repl.Replace(ln)))
	}
	return out
}

func TestLogRing_CapturesTrimsAndDropsBlanks(t *testing.T) {
	r := newLogRing(3)
	// A single write may contain several newline-terminated lines plus blanks.
	_, _ = r.Write([]byte("first\n\n  \nsecond\n"))
	_, _ = r.Write([]byte("third\nfourth\n"))

	got := r.recent()
	want := []string{"second", "third", "fourth"} // "first" trimmed by capacity
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("recent() = %v, want %v", got, want)
	}
}

func TestAgentFormatLogLine_Styling(t *testing.T) {
	cases := []struct {
		line         string
		wantPrefix   string
		wantContains string // ANSI color expected
	}{
		{"artifact renewal complete service=svc", "• ", "\x1b[32m"}, // green
		{"artifact renewal failed err=boom", "! ", "\x1b[33m"},      // yellow
		{"psk 80%: WARNING: server rotated", "· ", "\x1b[2m"},       // dim (warning)
		{"some neutral diagnostic", "· ", "\x1b[2m"},                // dim default
	}
	for _, c := range cases {
		got := agentFormatLogLine(c.line, 80)
		if !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("agentFormatLogLine(%q) prefix = %q, want %q", c.line, got[:2], c.wantPrefix)
		}
		if !strings.Contains(got, c.wantContains) {
			t.Errorf("agentFormatLogLine(%q) = %q, want it to contain %q", c.line, got, c.wantContains)
		}
	}
}

func TestRenderAgentView_AgentLogPanel(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.Out = &bytes.Buffer{} // not an *os.File → TerminalSize falls back to 100x40

	// A profile so the main panel has content.
	a.getOrCreateProfile("svc", "part", "dev")

	a.logs.append("artifact renewal complete service=svc participant=part")
	a.logs.append("psk_rotate: seed rotated to sB service=svc participant=part")

	frame := a.renderAgentView(agentViewState{})

	if !strings.Contains(frame, "Agent Log") {
		t.Fatal("rendered frame is missing the \"Agent Log\" panel title")
	}
	if strings.Contains(frame, "Routing Log") {
		t.Fatal("agent panel should be \"Agent Log\", not \"Routing Log\"")
	}
	if !strings.Contains(frame, "seed rotated to sB") {
		t.Fatal("rendered frame is missing a recent log line")
	}
	// Main panel uses the orange border; the log panel uses the gray border.
	if !strings.Contains(frame, "\x1b[38;5;208m") {
		t.Error("missing orange border (main panel)")
	}
	if !strings.Contains(frame, "\x1b[38;5;245m") {
		t.Error("missing gray border (Agent Log panel)")
	}
}

func TestRenderAgentView_AgentLogEmptyPlaceholder(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.Out = &bytes.Buffer{}
	a.getOrCreateProfile("svc", "part", "dev")

	frame := a.renderAgentView(agentViewState{})
	if !strings.Contains(frame, "Waiting for agent activity") {
		t.Fatal("empty Agent Log panel should show the waiting placeholder")
	}
}

// TestRenderAgentView_ReservesLastColumn verifies the fix for the missing
// right-edge orange border: the panel must not draw into the terminal's final
// column (width 100 → panel width 99), so the right border always renders.
func TestRenderAgentView_ReservesLastColumn(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.Out = &bytes.Buffer{} // → 100x40
	a.getOrCreateProfile("svc", "part", "dev")
	a.logs.append("artifact renewal complete service=svc")

	frame := a.renderAgentView(agentViewState{})

	maxWidth := 0
	for _, ln := range visibleFrameLines(frame) {
		if w := len([]rune(ln)); w > maxWidth {
			maxWidth = w
		}
	}
	if maxWidth != 99 {
		t.Fatalf("widest rendered line = %d cols, want 99 (terminal width 100 minus the reserved last column)", maxWidth)
	}
}
