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

// TestEmit_SummaryToRingDetailToStdout verifies that emit appends the short
// summary (severity-classified) to the ring and writes the verbose, [category]-
// tagged detail to stdout when the live TUI is not painting the terminal.
func TestEmit_SummaryToRingDetailToStdout(t *testing.T) {
	a := &Agent{logs: newLogRing(10)}
	var out bytes.Buffer
	a.Out = &out

	a.emit(agentEvent{
		cat:    catRenewal,
		sev:    tui.LogGood,
		detail: "artifact renewal complete service=svc participant=part artifact=device-cert old_not_after=x new_not_after=y",
	})

	entries := a.logs.recentEntries()
	if len(entries) != 1 {
		t.Fatalf("ring len = %d, want 1", len(entries))
	}
	if entries[0].text != "renewed device-cert (mTLS) for part" {
		t.Fatalf("ring summary = %q, want %q", entries[0].text, "renewed device-cert (mTLS) for part")
	}
	if !entries[0].classified || entries[0].sev != tui.LogGood {
		t.Fatalf("ring entry = %+v, want classified LogGood", entries[0])
	}
	if got := out.String(); !strings.Contains(got, "[renewal] artifact renewal complete service=svc") {
		t.Fatalf("stdout = %q, want tagged verbose detail", got)
	}
}

// TestEmit_TUIActiveWritesFileNotStdout verifies that while the TUI owns the
// terminal, emit writes the detail only to the file sink (LogOut) and the ring,
// never to stdout, which would corrupt the rendered frame.
func TestEmit_TUIActiveWritesFileNotStdout(t *testing.T) {
	a := &Agent{logs: newLogRing(10)}
	var out, logSink bytes.Buffer
	a.Out = &out
	a.LogOut = &logSink
	a.tuiActive.Store(true)

	a.emit(agentEvent{cat: catState, sev: tui.LogInfo, detail: "agent started inbox=/x"})

	if out.Len() != 0 {
		t.Fatalf("stdout should be empty while TUI active, got %q", out.String())
	}
	if !strings.Contains(logSink.String(), "[state] agent started inbox=/x") {
		t.Fatalf("file sink = %q, want tagged detail", logSink.String())
	}
	if len(a.logs.recentEntries()) != 1 {
		t.Fatal("ring should still capture the event while TUI active")
	}
}

// TestEmit_FileOnlyEventNotSurfaced verifies the filter policy (Step 3): a
// file-only event is recorded to the file sink but kept out of the panel ring.
func TestEmit_FileOnlyEventNotSurfaced(t *testing.T) {
	a := &Agent{logs: newLogRing(10)}
	var out bytes.Buffer
	a.Out = &out

	a.emit(agentEvent{cat: catSweep, sev: tui.LogInfo, detail: "sweep status state=active", fileOnly: true})

	if got := len(a.logs.recentEntries()); got != 0 {
		t.Fatalf("ring len = %d, want 0 (file-only event)", got)
	}
	if !strings.Contains(out.String(), "[sweep] sweep status state=active") {
		t.Fatalf("file-only event should still reach the file/stdout, got %q", out.String())
	}
}

// TestSummarizeAgentEvent covers Step 2: raw key=value detail → short phrase.
func TestSummarizeAgentEvent(t *testing.T) {
	cases := []struct {
		name string
		ev   agentEvent
		want string
	}{
		{"renewal started", agentEvent{cat: catRenewal, detail: "artifact renewal started service=svc participant=part artifact=identity reason=sweep"}, "renewing identity-cert for part"},
		{"renewal complete", agentEvent{cat: catRenewal, detail: "artifact renewal complete service=svc participant=part artifact=device-cert old_not_after=x new_not_after=y"}, "renewed device-cert (mTLS) for part"},
		{"renewal failed", agentEvent{cat: catRenewal, detail: "artifact renewal failed service=svc participant=part artifact=psk err=boom"}, "renew psk failed for part"},
		{"psk rotated", agentEvent{cat: catPSK, detail: "psk_rotate: seed rotated to sB service=svc participant=part"}, "rotated PSK for part"},
		{"psk cleaned", agentEvent{cat: catPSK, detail: "psk_cleanup: seed_extra cleared service=svc participant=part"}, "cleaned up PSK for part"},
		{"inbox enrolling", agentEvent{cat: catInbox, detail: "inbox enrollment request received service=svc participant=part"}, "enrolling part"},
		{"inbox bad file", agentEvent{cat: catInbox, detail: "inbox invalid JSON path=/x err=eof"}, "inbox: invalid enrollment request"},
		{"enrolled", agentEvent{cat: catEnroll, detail: "inbox enrollment complete service=svc participant=part"}, "enrolled part"},
		{"sweep state", agentEvent{cat: catSweep, detail: "sweep status service=svc participant=part state=active"}, "part active"},
		{"rehydrated", agentEvent{cat: catState, detail: "profile rehydrated serial=s service=svc domain=d participant=part device=dev state=active"}, "loaded part (active)"},
		{"unrecognized falls back", agentEvent{cat: catWarning, detail: "Warning: could not persist agent state: disk full"}, "Warning: could not persist agent state: disk full"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := summarizeAgentEvent(c.ev); got != c.want {
				t.Fatalf("summarizeAgentEvent = %q, want %q", got, c.want)
			}
		})
	}
}

func TestSurfaceInPanel(t *testing.T) {
	if !surfaceInPanel(agentEvent{cat: catRenewal}) {
		t.Error("non-file-only event should surface")
	}
	if surfaceInPanel(agentEvent{cat: catSweep, fileOnly: true}) {
		t.Error("file-only event should not surface")
	}
}

// TestSweep_SurfacesOnStateChangeOnly is the Step 3 integration guard: the first
// sweep surfaces the state, an unchanged second sweep does not, and a changed
// third sweep surfaces again.
func TestSweep_SurfacesOnStateChangeOnly(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.Out = &bytes.Buffer{}
	p := a.getOrCreateProfile("svc", "part", "SN-001", "dev")
	p.state = StateActive

	a.sweep()
	if got := len(a.logs.recentEntries()); got != 1 {
		t.Fatalf("after first sweep ring len = %d, want 1", got)
	}
	a.sweep() // unchanged → file-only
	if got := len(a.logs.recentEntries()); got != 1 {
		t.Fatalf("after unchanged sweep ring len = %d, want 1 (no new panel line)", got)
	}
	p.state = StateRenewing
	a.sweep() // changed → surfaces again
	if got := len(a.logs.recentEntries()); got != 2 {
		t.Fatalf("after changed sweep ring len = %d, want 2", got)
	}
}

// TestRenderAgentView_ShowsSummaryAndAge verifies the panel renders the short
// summary (Step 2) with a leading absolute timestamp of the event's arrival
// time.
func TestRenderAgentView_ShowsSummaryAndAge(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.Out = &bytes.Buffer{}
	a.getOrCreateProfile("svc", "part", "SN-001", "dev")

	base := time.Now()
	a.Now = func() time.Time { return base }
	a.emit(agentEvent{
		cat:    catRenewal,
		sev:    tui.LogGood,
		detail: "artifact renewal complete service=svc participant=part artifact=device-cert old_not_after=x new_not_after=y",
	})

	// Advance the render clock past the event's arrival time; the timestamp
	// shown is the event's own arrival time, not the render clock.
	a.Now = func() time.Time { return base.Add(30 * time.Second) }
	plain := tui.StripANSIEscapes(a.renderAgentView(agentViewState{}))

	if !strings.Contains(plain, "renewed device-cert (mTLS) for part") {
		t.Fatalf("panel missing summary; frame:\n%s", plain)
	}
	if !strings.Contains(plain, base.Format("15:04:05")) {
		t.Fatalf("panel missing leading timestamp %s; frame:\n%s", base.Format("15:04:05"), plain)
	}
}
