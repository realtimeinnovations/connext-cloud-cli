// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"fmt"
	"io"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

// agentCategory is the small, closed taxonomy of agent lifecycle events. It is
// the analogue of the gateway's [stream]/[route]/[lost] tags: it tags each line
// written to the log file and is the single source of truth shared by the
// emitter, the file format, the panel summarizer, and the surface filter.
type agentCategory string

const (
	catEnroll  agentCategory = "enroll"  // inbox/enrollment lifecycle
	catInbox   agentCategory = "inbox"   // inbox file intake/validation
	catRenewal agentCategory = "renewal" // certificate / mTLS / lease renewal
	catPSK     agentCategory = "psk"     // PSK rotate / cleanup phases
	catSweep   agentCategory = "sweep"   // periodic state sweep
	catState   agentCategory = "state"   // rehydrate / persist / lifecycle
	catWarning agentCategory = "warning" // non-fatal problems
)

// agentEvent is one structured agent lifecycle event. detail is the verbose
// key=value line written to the log file; the panel shows a short summary
// derived by summarizeAgentEvent. fileOnly marks routine events (e.g. unchanged
// sweep heartbeats) that should be recorded to the file but not the panel.
type agentEvent struct {
	cat      agentCategory
	sev      tui.LogSeverity
	detail   string
	fileOnly bool
}

// emit is the single choke point for agent lifecycle events. It records the
// verbose detail (with a [category] tag) to the log file — and, when the live
// TUI is not painting the terminal, to stdout so pipes/systemd still see it —
// and appends a short, severity-classified summary to the in-memory ring that
// backs the TUI "Agent Log" panel (unless the event is file-only).
//
// Routing every event through emit is the agent's analogue of the gateway's
// single HandleLine path: capture, filter, and summarize all happen in one place
// so the panel can no longer miss events or show raw machine output.
func (a *Agent) emit(ev agentEvent) {
	if a.logs != nil && surfaceInPanel(ev) {
		a.logs.appendEvent(summarizeAgentEvent(ev), ev.sev, a.clockNow())
	}
	tagged := "[" + string(ev.cat) + "] " + ev.detail + "\n"

	// Prefer the raw sinks (set by Run) so we can serialize via outMu without
	// re-entering the syncWriter wrappers on Out/LogOut; fall back to those
	// wrappers when Run has not wired the raw sinks (e.g. unit tests).
	var sink io.Writer
	if a.tuiActive.Load() {
		if sink = a.eventFile; sink == nil {
			sink = a.LogOut
		}
	} else {
		if sink = a.eventStdout; sink == nil {
			sink = a.Out
		}
	}
	if sink == nil {
		return
	}

	a.outMu.Lock()
	defer a.outMu.Unlock()
	_, _ = io.WriteString(sink, tagged)
}

// emitf is a Printf-style convenience wrapper around emit for events whose
// detail is built from a format string and that always surface in the panel.
func (a *Agent) emitf(cat agentCategory, sev tui.LogSeverity, format string, args ...any) {
	a.emit(agentEvent{cat: cat, sev: sev, detail: fmt.Sprintf(format, args...)})
}

// surfaceInPanel is the filter policy (the agent's isLiveLogLine): file-only
// events are recorded but kept out of the panel so routine, high-frequency
// heartbeats do not drown out meaningful lifecycle activity.
func surfaceInPanel(ev agentEvent) bool {
	return !ev.fileOnly
}

// summarizeAgentEvent turns a verbose key=value detail line into a short, human
// phrase for the panel (the agent's summarizeLogLine). It parses the well-known
// fields the agent emits; anything unrecognized falls back to the trimmed
// detail, so no event is ever shown blank.
func summarizeAgentEvent(ev agentEvent) string {
	d := ev.detail
	participant := eventField(d, "participant")
	switch ev.cat {
	case catRenewal:
		label := artifactLabel[ArtifactID(eventField(d, "artifact"))]
		switch {
		case strings.HasPrefix(d, "artifact renewal started") && label != "" && participant != "":
			return "renewing " + label + " for " + participant
		case strings.HasPrefix(d, "artifact renewal complete") && label != "" && participant != "":
			return "renewed " + label + " for " + participant
		case strings.HasPrefix(d, "artifact renewal failed") && label != "" && participant != "":
			return "renew " + label + " failed for " + participant
		}
	case catPSK:
		switch {
		case strings.HasPrefix(d, "psk_rotate: seed rotated") && participant != "":
			return "rotated PSK for " + participant
		case strings.HasPrefix(d, "psk_cleanup: seed_extra cleared") && participant != "":
			return "cleaned up PSK for " + participant
		case strings.Contains(d, "server has already rotated") && participant != "":
			return "PSK already rotated server-side for " + participant
		}
	case catInbox:
		switch {
		case strings.HasPrefix(d, "inbox enrollment request received") && participant != "":
			return "enrolling " + participant
		case strings.HasPrefix(d, "inbox enrollment failed") && participant != "":
			return "enroll " + participant + " failed"
		case strings.HasPrefix(d, "inbox read failed"),
			strings.HasPrefix(d, "inbox invalid JSON"),
			strings.HasPrefix(d, "inbox missing required fields"):
			return "inbox: invalid enrollment request"
		}
	case catEnroll:
		switch {
		case strings.HasPrefix(d, "inbox enrollment complete") && participant != "":
			return "enrolled " + participant
		case strings.HasPrefix(d, "Note: device already enrolled"):
			return "device already enrolled"
		}
	case catSweep:
		if state := eventField(d, "state"); participant != "" && state != "" {
			return participant + " " + state
		}
	case catState:
		switch {
		case strings.HasPrefix(d, "profile rehydrated"):
			if state := eventField(d, "state"); participant != "" && state != "" {
				return "loaded " + participant + " (" + state + ")"
			}
		case strings.HasPrefix(d, "agent started"):
			return "agent started"
		}
	}
	return strings.TrimSpace(d)
}

// eventField extracts the value of a "key=value" token from a detail line,
// returning the text up to the next space (or end of string). Empty if absent.
func eventField(s, key string) string {
	pre := key + "="
	i := strings.Index(s, pre)
	if i < 0 {
		return ""
	}
	rest := s[i+len(pre):]
	if j := strings.IndexByte(rest, ' '); j >= 0 {
		return rest[:j]
	}
	return rest
}
