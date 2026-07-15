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
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

// TestAgentSummaryChip_RevokedFitsStatusWidth guards against silent
// truncation: agentSummaryStatusWidth is a fixed column width (not derived
// from terminal size), so tui.StyleChipWidth/PadDisplay truncates — with an
// ellipsis, mid-word — any chip text that doesn't fit, regardless of how wide
// the terminal actually is.
func TestAgentSummaryChip_RevokedFitsStatusWidth(t *testing.T) {
	for _, profiles := range []int{1, 2, 9, 42, 99} {
		for _, revoked := range []int{1, profiles} {
			got := agentSummaryChip(profiles, 0, revoked)
			if w := tui.DisplayWidth(got); w > agentSummaryStatusWidth {
				t.Errorf("agentSummaryChip(%d,_,%d) = %q (visible width %d) exceeds agentSummaryStatusWidth=%d and will be truncated",
					profiles, revoked, got, w, agentSummaryStatusWidth)
			}
		}
	}
}

// TestRenderAgentView_RevokedSelectedRowKeepsHighlight guards against a
// layout regression: tui.RenderPanel's panelBodyLine falls back to stripping
// ALL ANSI styling from a line whose visible width exceeds the panel's
// content width. A revoked row's hint text must stay short enough that the
// selected/highlighted revoked row keeps both its red "revoked" styling and
// its gray row-highlight background instead of silently losing both.
func TestRenderAgentView_RevokedSelectedRowKeepsHighlight(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.Now = func() time.Time { return time.Unix(0, 0) }

	p := a.getOrCreateProfile("svc", "part", "SN-001", "dev")
	p.mu.Lock()
	p.serial = "SN-001"
	p.domainTemplateID = "dom"
	p.state = StateRevoked
	p.notAfter[ArtifactPermissions] = time.Unix(0, 0).Add(100 * time.Second)
	p.issuedAt[ArtifactPermissions] = time.Unix(0, 0)
	p.mu.Unlock()

	// rowSel=1 selects ArtifactPermissions (the second entry in
	// displayArtifacts), a node-scoped artifact that renders as "revoked".
	vs := agentViewState{activeTab: 0, rowSel: 1, focus: focusTable}
	out := a.renderAgentView(vs)

	lines := strings.Split(out, "\r\n")
	var permissionsLine string
	for _, l := range lines {
		if strings.Contains(l, "permissions") {
			permissionsLine = l
			break
		}
	}
	if permissionsLine == "" {
		t.Fatal("could not find the permissions row in rendered output")
	}
	if !strings.Contains(permissionsLine, "\x1b[48;5;238m") {
		t.Errorf("selected revoked row lost its gray highlight background: %q", permissionsLine)
	}
	if !strings.Contains(permissionsLine, "\x1b[1;31m") {
		t.Errorf("selected revoked row lost its red \"revoked\" styling: %q", permissionsLine)
	}
}

func TestAgentFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{-1 * time.Second, "needs renewal"},
		{0, "0s"},
		{45 * time.Second, "45s"},
		{59*time.Second + 999*time.Millisecond, "59s"},
		{time.Minute, "1m 0s"},
		{12*time.Minute + 5*time.Second, "12m 5s"},
		{59*time.Minute + 59*time.Second, "59m 59s"},
		{time.Hour, "1h 0m"},
		{5*time.Hour + 20*time.Minute + 30*time.Second, "5h 20m"},
		{23*time.Hour + 59*time.Minute, "23h 59m"},
		{24 * time.Hour, "1d 0h 0m"},
		{2*24*time.Hour + 3*time.Hour + 15*time.Minute, "2d 3h 15m"},
		{155*time.Hour + 6*time.Minute, "6d 11h 6m"},
		{29*24*time.Hour + 23*time.Hour, "29d 23h 0m"},
		{30 * 24 * time.Hour, "1mo 0d"},
		{45 * 24 * time.Hour, "1mo 15d"},
		{3*30*24*time.Hour + 12*24*time.Hour, "3mo 12d"},
		{364 * 24 * time.Hour, "12mo 4d"},
		{365 * 24 * time.Hour, "1y 0d"},
		{400 * 24 * time.Hour, "1y 35d"},
		{2 * 365 * 24 * time.Hour, "2y 0d"},
	}
	for _, c := range cases {
		if got := agentFormatDuration(c.d); got != c.want {
			t.Errorf("agentFormatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

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
	a.getOrCreateProfile("svc", "part", "SN-001", "dev")

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
	a.getOrCreateProfile("svc", "part", "SN-001", "dev")

	frame := a.renderAgentView(agentViewState{})
	if !strings.Contains(frame, "Waiting for agent activity") {
		t.Fatal("empty Agent Log panel should show the waiting placeholder")
	}
}

func TestAgentSummaryChip(t *testing.T) {
	cases := []struct {
		profiles     int
		needsRenewal int
		revoked      int
		wantText     string
		wantColor    string // markup tag resolved by tui.StyleChipWidth
	}{
		{0, 0, 0, "waiting for enrollment", "[dim]"},
		{1, 0, 0, "1 participant artifact monitored", "[green]"},
		{3, 0, 0, "3 participant artifacts monitored", "[green]"},
		{3, 2, 0, "3 participant artifacts monitored", "[yellow]"},
		{3, 2, 1, "1 of 3 participants revoked", "[red]"},
	}
	for _, c := range cases {
		got := agentSummaryChip(c.profiles, c.needsRenewal, c.revoked)
		if !strings.Contains(got, c.wantText) {
			t.Errorf("agentSummaryChip(%d,%d,%d) = %q, want it to contain %q", c.profiles, c.needsRenewal, c.revoked, got, c.wantText)
		}
		if !strings.Contains(got, c.wantColor) {
			t.Errorf("agentSummaryChip(%d,%d,%d) = %q, want color %q", c.profiles, c.needsRenewal, c.revoked, got, c.wantColor)
		}
	}
}

func TestCountArtifactsNeedingRenewal(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	profiles := []profileSnapshot{
		{ // healthy: only 10% of lifetime elapsed
			notAfter: map[ArtifactID]time.Time{ArtifactIdentity: now.Add(90 * time.Hour)},
			issuedAt: map[ArtifactID]time.Time{ArtifactIdentity: now.Add(-10 * time.Hour)},
		},
		{ // past the 80% threshold → needs renewal
			notAfter: map[ArtifactID]time.Time{ArtifactPermissions: now.Add(1 * time.Hour)},
			issuedAt: map[ArtifactID]time.Time{ArtifactPermissions: now.Add(-99 * time.Hour)},
		},
		{ // no expiry recorded → ignored
			notAfter: map[ArtifactID]time.Time{},
			issuedAt: map[ArtifactID]time.Time{},
		},
	}
	if got := countArtifactsNeedingRenewal(now, profiles); got != 1 {
		t.Fatalf("countArtifactsNeedingRenewal = %d, want 1", got)
	}
}

// TestSnapshotProfiles_MirrorsDomainArtifactsToNonOwner verifies that the
// domain owner's PSK/CRL renewal and expiry times are mirrored onto the other
// participants in the same domain, so every participant shows the shared
// artifact rather than "–".
func TestSnapshotProfiles_MirrorsDomainArtifactsToNonOwner(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	now := time.Unix(1_000_000, 0)
	a.Now = func() time.Time { return now }

	// Owner: tracks the domain-scoped PSK and CRL.
	owner := a.getOrCreateProfile("svc", "tractor", "SN-owner", "")
	owner.domainTemplateID = "29:south-field"
	owner.notAfter[ArtifactPSK] = now.Add(48 * time.Hour)
	owner.issuedAt[ArtifactPSK] = now.Add(-2 * time.Hour)
	owner.notAfter[ArtifactPSKRotate] = now.Add(50 * time.Hour)
	owner.notAfter[ArtifactCRL] = now.Add(5 * time.Minute)
	owner.issuedAt[ArtifactCRL] = now

	// Non-owner: same domain + participant template, no PSK/CRL of its own.
	other := a.getOrCreateProfile("svc", "tractor", "SN-other", "")
	other.domainTemplateID = "29:south-field"
	other.notAfter[ArtifactIdentity] = now.Add(24 * time.Hour)

	a.claimDomainOwner(owner)

	var got profileSnapshot
	found := false
	for _, s := range a.snapshotProfiles() {
		if s.serial == "SN-other" {
			got = s
			found = true
			break
		}
	}
	if !found {
		t.Fatal("non-owner snapshot not found")
	}
	for _, art := range []ArtifactID{ArtifactPSK, ArtifactPSKRotate, ArtifactCRL} {
		na, ok := got.notAfter[art]
		if !ok || !na.Equal(owner.notAfter[art]) {
			t.Fatalf("artifact %s not mirrored onto non-owner: got %v, want %v", art, na, owner.notAfter[art])
		}
	}
	if is := got.issuedAt[ArtifactPSK]; !is.Equal(owner.issuedAt[ArtifactPSK]) {
		t.Fatalf("PSK issuedAt not mirrored: got %v, want %v", is, owner.issuedAt[ArtifactPSK])
	}
}

// TestRenderAgentView_SummaryAndDetailsSections verifies the two-section layout:
// an orange summary panel carrying the status chip, followed by a separate blue
// "Participants" details panel (mirroring the gateway/spy TUIs).
func TestRenderAgentView_SummaryAndDetailsSections(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.Out = &bytes.Buffer{} // → 100x40
	a.getOrCreateProfile("svc", "part", "SN-001", "dev")

	frame := a.renderAgentView(agentViewState{})
	lines := visibleFrameLines(frame)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "Edge-Sync Agent") {
		t.Error("missing summary panel title \"Edge-Sync Agent\"")
	}
	if !strings.Contains(joined, "Participants") {
		t.Error("missing details panel title \"Participants\"")
	}
	if !strings.Contains(joined, "participant artifact monitored") {
		t.Error("missing status chip text in summary section")
	}
	// The summary panel must come before the details panel.
	summaryIdx, detailsIdx := -1, -1
	for i, ln := range lines {
		if summaryIdx == -1 && strings.Contains(ln, "Edge-Sync Agent") {
			summaryIdx = i
		}
		if detailsIdx == -1 && strings.Contains(ln, "Participants") {
			detailsIdx = i
		}
	}
	if summaryIdx == -1 || detailsIdx == -1 || summaryIdx >= detailsIdx {
		t.Fatalf("expected summary section above details section, got summary=%d details=%d", summaryIdx, detailsIdx)
	}
	// Blue details border and orange summary border must both be present.
	if !strings.Contains(frame, "\x1b[38;5;110m") {
		t.Error("missing blue border (details panel)")
	}
	if !strings.Contains(frame, "\x1b[38;5;208m") {
		t.Error("missing orange border (summary panel)")
	}
}

// TestRenderAgentView_ReservesLastColumn verifies the fix for the missing
// right-edge orange border: the panel must not draw into the terminal's final
// column (width 100 → panel width 99), so the right border always renders.
func TestRenderAgentView_ReservesLastColumn(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.Out = &bytes.Buffer{} // → 100x40
	a.getOrCreateProfile("svc", "part", "SN-001", "dev")
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
