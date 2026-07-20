// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

const (
	agentDefaultWidth  = 100
	agentDefaultHeight = 40
	colArtifactW       = 22
	colRenewalW        = 22
	colExpirationW     = 22

	// Summary panel column widths (label + status chip), matching the layout
	// of the gateway/spy summary panels.
	agentSummaryLabelWidth  = 12
	agentSummaryStatusWidth = 40

	// rowHighlightBg is the background applied to the focused artifact row.
	rowHighlightBg = "\x1b[48;5;238m"

	// tabActiveLabelMax caps the expanded name shown on the selected profile.
	tabActiveLabelMax = 24

	// orangeFg is the foreground used for the selected profile's outline.
	orangeFg = "\x1b[38;5;208m"
)

// artifactLabel maps each ArtifactID to its display label.
var artifactLabel = map[ArtifactID]string{
	ArtifactDeviceCert:  "device-cert (mTLS)",
	ArtifactPSK:         "psk",
	ArtifactIdentity:    "identity-cert",
	ArtifactPermissions: "permissions",
	ArtifactCRL:         "crl",
}

// focusZone identifies which interactive region currently has keyboard focus.
type focusZone int

const (
	focusTable   focusZone = iota // artifact table: Up/Down select, Enter renews
	focusButtons                  // action buttons: Left/Right select, Enter activates
)

// Button indices within the action bar.
const (
	btnStop = 0
	btnAdd  = 1
)

// profileSnapshot is a point-in-time copy of a profile's display state.
// It is used by both renderAgentView and the renew handler so the same
// sorted slice drives both without re-walking the sync.Map.
type profileSnapshot struct {
	serial           string
	serviceID        string
	domainTemplateID string
	participantID    string
	deviceName       string
	state            ProfileState
	notAfter         map[ArtifactID]time.Time
	issuedAt         map[ArtifactID]time.Time
}

func (p profileSnapshot) key() string {
	return p.serviceID + "/" + p.participantID + "/" + p.serial
}

func (a *Agent) snapshotProfiles() []profileSnapshot {
	var out []profileSnapshot
	// domainShared holds each domain owner's domain-scoped artifact times
	// (PSK, CRL, PSK phases) keyed by (service, domain), so that non-owner
	// participants sharing that domain can mirror them for display.
	domainShared := map[string]profileSnapshot{}
	a.profiles.Range(func(_, val any) bool {
		p := val.(*profile)
		owner := a.isDomainOwner(p)
		p.mu.Lock()
		na := make(map[ArtifactID]time.Time, len(p.notAfter))
		for k, v := range p.notAfter {
			na[k] = v
		}
		is := make(map[ArtifactID]time.Time, len(p.issuedAt))
		for k, v := range p.issuedAt {
			is[k] = v
		}
		snap := profileSnapshot{p.serial, p.serviceID, p.domain(), p.participantID, p.deviceName, p.state, na, is}
		p.mu.Unlock()
		if owner {
			domainShared[domainOwnerKey(snap.serviceID, snap.domainTemplateID)] = snap
		}
		out = append(out, snap)
		return true
	})
	mirrorDomainArtifacts(out, domainShared)
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// mirrorDomainArtifacts copies each domain owner's domain-scoped artifact times
// (PSK, CRL and the PSK phase markers) onto the other participants in the same
// domain that lack them. PSK and CRL are managed once per domain by the owner,
// but every participant shares the resulting files, so the renewal/expiry is
// shown on all of them — and a manual renew from any participant is redirected
// to the owner (see RenewArtifact). Entries a participant already has (i.e. the
// owner itself) are left untouched.
func mirrorDomainArtifacts(snaps []profileSnapshot, domainShared map[string]profileSnapshot) {
	for i := range snaps {
		owner, ok := domainShared[domainOwnerKey(snaps[i].serviceID, snaps[i].domainTemplateID)]
		if !ok {
			continue
		}
		for art, na := range owner.notAfter {
			if !isDomainArtifact(art) {
				continue
			}
			if _, has := snaps[i].notAfter[art]; has {
				continue
			}
			snaps[i].notAfter[art] = na
			if is, ok := owner.issuedAt[art]; ok {
				snaps[i].issuedAt[art] = is
			}
		}
	}
}

// agentViewState holds the mutable TUI navigation state for runDisplay.
type agentViewState struct {
	activeTab int
	rowSel    int
	focus     focusZone
	btnSel    int
}

// truncateLabel shortens s to at most max runes, appending "…" if truncated.
func truncateLabel(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// renderTabBar returns the tab-bar body line for the profile list.
// At most maxVisible chips are shown at once, windowed around the active tab.
//
// The active profile is drawn with an orange outline, bold, and its full
// (un-truncated) name so it stands out; inactive profiles collapse to just
// their 1-based number inside a dim grey outline.
func renderTabBar(profiles []profileSnapshot, active int) string {
	const maxVisible = 9
	n := len(profiles)
	if n == 0 {
		return ""
	}
	start := active - maxVisible/2
	if start < 0 {
		start = 0
	}
	if start+maxVisible > n {
		start = n - maxVisible
	}
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > n {
		end = n
	}

	chips := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		num := fmt.Sprintf("%d", i+1)
		revoked := profiles[i].state == StateRevoked
		if i == active {
			// Prefer the device name, then the serial (unique per node), then
			// the participant template. The serial fallback keeps nodes enrolled
			// from the same participant template visually distinct.
			label := profiles[i].deviceName
			if label == "" {
				label = profiles[i].serial
			}
			if label == "" {
				label = profiles[i].participantID
			}
			label = truncateLabel(label, tabActiveLabelMax)
			if revoked {
				chips = append(chips, "\x1b[1;31m[ "+num+" ✗ "+label+" ]\x1b[0m")
			} else {
				chips = append(chips, orangeFg+"\x1b[1m[ "+num+" "+label+" ]\x1b[0m")
			}
		} else if revoked {
			chips = append(chips, "\x1b[31m[ "+num+" ✗ ]\x1b[0m")
		} else {
			chips = append(chips, tui.Dim("[ "+num+" ]"))
		}
	}

	var sb strings.Builder
	sb.WriteString("  ")
	if start > 0 {
		sb.WriteString(tui.Dim("‹ "))
	}
	sb.WriteString(strings.Join(chips, " "))
	if end < n {
		sb.WriteString(tui.Dim(" ›"))
	}
	return sb.String()
}

// agentBtn renders one action button. focused reports whether the buttons
// zone currently holds focus; sel is the selected button index within it.
// A button is shown in its active (highlighted) style only when the buttons
// zone is focused AND it is the selected one.
func agentBtn(idx, sel int, focused bool) string {
	type def struct {
		key, label         string
		normalBg, normalFg string
		activeBg           string
	}
	defs := []def{
		{"^C", "Stop", "\x1b[48;5;52m", "\x1b[38;5;208m", "\x1b[48;5;208m\x1b[1;38;5;232m"},
		{"^A", "Add participant", "\x1b[48;5;23m", "\x1b[38;5;114m", "\x1b[48;5;114m\x1b[1;38;5;232m"},
	}
	d := defs[idx]
	if focused && idx == sel {
		return d.activeBg + " " + d.key + "  " + d.label + " \x1b[0m"
	}
	return d.normalBg + d.normalFg + " " + d.key + " \x1b[0m " + tui.Dim(d.label) + " "
}

// renderAgentView builds the full ANSI escape string for one refresh cycle.
func (a *Agent) renderAgentView(vs agentViewState) string {
	now := a.Now()
	profiles := a.snapshotProfiles()

	if vs.activeTab >= len(profiles) {
		vs.activeTab = len(profiles) - 1
	}
	if vs.activeTab < 0 {
		vs.activeTab = 0
	}

	tw := a.termOut
	if tw == nil {
		tw = a.Out
	}
	width, height := tui.TerminalSize(tw, agentDefaultWidth, agentDefaultHeight)
	// Reserve the last terminal column so the panel's right border never lands
	// in the auto-wrap column.  Drawing into the final column, combined with
	// framePaint's per-line erase-to-EOL, is why the orange vertical line on
	// the right edge did not show in the terminal.
	panelWidth := tui.MaxInt(12, width-1)
	contentWidth := tui.MaxInt(60, panelWidth-4)

	// ── Summary section: a short status chip, mirroring the gateway/spy TUIs.
	// Lifecycle events and manual-renew outcomes are surfaced in the Agent Log
	// panel below (via emit), so they are intentionally not duplicated here.
	needsRenewal := countArtifactsNeedingRenewal(now, profiles)
	revokedCount := countRevoked(profiles)
	summaryBody := []string{
		formatAgentSummaryLine("agent", agentSummaryChip(len(profiles), needsRenewal, revokedCount), "Logs: "+a.LogFile, contentWidth),
	}

	// ── Details section: the participant list and their renewal artifacts. ──
	var body []string
	if len(profiles) == 0 {
		body = append(body, tui.Dim("  (waiting for enrollment — see hint below)"))
	} else {
		body = append(body, renderTabBar(profiles, vs.activeTab))
		body = append(body, "")

		pr := profiles[vs.activeTab]

		// ── Identifiers section ──
		body = append(body, tui.StyleSection("Identifiers"))
		body = append(body, tui.Dim("  Deployment Name:      ")+pr.serial)
		body = append(body, tui.Dim("  Edge Provision Svc:   ")+pr.serviceID)
		body = append(body, tui.Dim("  Domain Template:      ")+pr.domainTemplateID)
		body = append(body, tui.Dim("  Participant Template: ")+pr.participantID)
		if pr.state == StateRevoked {
			body = append(body, tui.Dim("  Status:               ")+
				"\x1b[1;31m✗ REVOKED — credentials rejected by the server. Re-enroll to restore this participant\x1b[0m")
		}
		body = append(body, "")

		// ── Artifact renewal table ──
		artHeader := "  " + tui.StyleLabel("ARTIFACT", colArtifactW) + "  " +
			tui.StyleLabel("RENEWS IN", colRenewalW) + "  " +
			tui.StyleLabel("EXPIRES AT", colExpirationW)
		artSep := "  " + tui.Dim(strings.Repeat("─", colArtifactW+2+colRenewalW+2+colExpirationW))
		body = append(body, artHeader, artSep)

		for rowIdx, art := range displayArtifacts {
			// This participant's own credentials were rejected, so it cannot
			// itself renew ANY artifact — including PSK/CRL: another, still
			// valid participant may keep the shared domain secrets renewed
			// (see failoverDomainOwner), but this one, on its own, cannot.
			revokedRow := pr.state == StateRevoked
			na, ok := pr.notAfter[art]
			var renewCell string
			switch {
			case revokedRow:
				renewCell = "\x1b[1;31m" + tui.PadDisplay("revoked", colRenewalW) + "\x1b[0m"
			case !ok || na.IsZero():
				renewCell = tui.Dim(tui.PadDisplay("–", colRenewalW))
			default:
				renewCell = agentRenewalCell(now, pr.issuedAt[art], na)
			}
			expiryCell := agentExpirationCell(art, pr.notAfter)
			artName := tui.PadDisplay(artifactLabel[art], colArtifactW)
			cells := artName + "  " + renewCell + "  " + expiryCell

			// Keep the hint text the same length regardless of state: a longer
			// revoked-specific hint pushes the row's visible width past the
			// panel's content width, which makes tui.RenderPanel fall back to
			// stripping ALL ANSI styling from the line — silently dropping both
			// the red "revoked" cell color and the row highlight background.
			hint := "\x1b[1mPress Enter to renew\x1b[0m"
			if revokedRow {
				hint = "\x1b[1mRevoked — retry\x1b[0m"
			}
			switch {
			case rowIdx == vs.rowSel && vs.focus == focusTable:
				// Full-line background highlight. Each embedded \x1b[0m inside
				// the colored cells is rewritten to also re-apply the row
				// background, so the bar does not break at the first reset.
				inner := strings.ReplaceAll("  "+cells, "\x1b[0m", "\x1b[0m"+rowHighlightBg)
				body = append(body, rowHighlightBg+inner+"  "+hint)
			case rowIdx == vs.rowSel:
				// Selected but table not focused: keep a subtle marker only.
				body = append(body, "\x1b[38;5;208m› \x1b[0m"+cells)
			default:
				body = append(body, "  "+cells)
			}
		}
	}

	// Action buttons (always shown so they remain reachable with 0 profiles).
	body = append(body, "")
	body = append(body, "  "+
		agentBtn(btnStop, vs.btnSel, vs.focus == focusButtons)+"   "+
		agentBtn(btnAdd, vs.btnSel, vs.focus == focusButtons))

	body = append(body, tui.Dim("  ↑↓ artifact  ·  Tab participant  ·  ←→ buttons  ·  Enter confirm  ·  ^C stop  ·  ^A add"))

	// Stack the sections like the gateway/spy TUIs: a short orange summary panel
	// with the status chip, a blank separator, then the blue details panel and
	// the gray Agent Log panel below.
	summaryPanel := tui.RenderPanel("Edge-Sync Agent", summaryBody, panelWidth, agentSummaryPanelTheme())
	detailsPanel := tui.RenderPanel("Participants", body, panelWidth, agentDetailsPanelTheme())

	panelLines := append([]string{""}, summaryPanel...)
	panelLines = append(panelLines, "")
	panelLines = append(panelLines, detailsPanel...)
	panelLines = append(panelLines, a.renderAgentLogPanel(panelWidth, contentWidth, height-len(panelLines))...)
	return framePaint(panelLines)
}

// countArtifactsNeedingRenewal counts, across all profiles, the artifacts whose
// 80% renewal threshold has already passed (RenewalDelay == 0 for a known
// expiry). This is the same condition that colors a row red in the details
// table, so the summary chip and the table agree.
func countArtifactsNeedingRenewal(now time.Time, profiles []profileSnapshot) int {
	count := 0
	for _, p := range profiles {
		for _, art := range displayArtifacts {
			na, ok := p.notAfter[art]
			if !ok || na.IsZero() {
				continue
			}
			if RenewalDelay(now, p.issuedAt[art], na) <= 0 {
				count++
			}
		}
	}
	return count
}

// countRevoked counts the profiles currently marked StateRevoked (their own
// mTLS credentials were rejected by the Provisioning Service).
func countRevoked(profiles []profileSnapshot) int {
	count := 0
	for _, p := range profiles {
		if p.state == StateRevoked {
			count++
		}
	}
	return count
}

// agentSummaryChip renders the status chip shown in the summary panel, using
// the same [color] markup convention as the gateway/spy chips (resolved by
// tui.StyleChipWidth): dim when idle, red when any participant is revoked
// (highest priority — it needs attention no renewal retry will fix), yellow
// when one or more artifacts are due for renewal, green when healthy.
func agentSummaryChip(profileCount, needsRenewal, revokedCount int) string {
	if profileCount == 0 {
		return "[dim]○ waiting for enrollment[/dim]"
	}
	phrase := fmt.Sprintf("%d %s monitored", profileCount, agentPluralize(profileCount, "participant artifact"))
	if revokedCount > 0 {
		// Kept short deliberately: agentSummaryStatusWidth is a fixed column
		// width (not tied to terminal size), so a longer message here gets
		// silently truncated by tui.StyleChipWidth/PadDisplay.
		return fmt.Sprintf("[red]✗ %d of %d %s revoked[/red]", revokedCount, profileCount, agentPluralize(profileCount, "participant"))
	}
	if needsRenewal > 0 {
		return fmt.Sprintf("[yellow]● %s[/yellow]", phrase)
	}
	return fmt.Sprintf("[green]● %s[/green]", phrase)
}

// formatAgentSummaryLine lays out one summary line as LABEL + status chip +
// target, matching the gateway/spy summary panels.
func formatAgentSummaryLine(label, chip, target string, contentWidth int) string {
	l := tui.StyleLabel(strings.ToUpper(label), agentSummaryLabelWidth)
	targetWidth := tui.MaxInt(8, contentWidth-agentSummaryLabelWidth-agentSummaryStatusWidth-4)
	status := tui.StyleChipWidth(chip, agentSummaryStatusWidth)
	tgt := tui.StyleTarget(tui.TruncateDisplay(target, targetWidth), targetWidth)
	return fmt.Sprintf("%s  %s  %s", l, status, tgt)
}

// agentPluralize appends an "s" to noun unless n == 1.
func agentPluralize(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// agentSummaryPanelTheme is the orange, padded summary panel (matches the
// gateway/spy summary panels).
func agentSummaryPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleTitle, BorderStyle: tui.StyleOrangeBorder, PaddedBody: true}
}

// agentDetailsPanelTheme is the blue-bordered details panel that holds the
// participant list and renewal table (matches the gateway "Routes" panel).
func agentDetailsPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleSection, BorderStyle: tui.StyleBlueBorder, PaddedBody: true}
}

// renderAgentLogPanel renders the "Agent Log" panel that sits beneath the main
// panel, mirroring the gateway "Routing Log" panel's style and colors.  It shows
// the most recent log lines that fit within remainingHeight and returns nil (no
// separator, no panel) when there is no vertical room.
func (a *Agent) renderAgentLogPanel(panelWidth, contentWidth, remainingHeight int) []string {
	entries := tui.CompactLogEntries(a.agentLogEntries())
	total := len(entries)
	if total == 0 {
		entries = []tui.LogEntry{{Text: "Waiting for agent activity..."}}
		total = 1
	}

	// Overhead below the main panel: 1 blank separator + 2 panel borders.
	budget := tui.ClampLogBudget(remainingHeight-3, total, tui.LogPanelMaxLines)
	if budget < 1 {
		return nil
	}
	if len(entries) > budget {
		entries = entries[len(entries)-budget:]
	}

	body := make([]string, 0, len(entries))
	for _, e := range entries {
		body = append(body, tui.FormatLogEntry(e, contentWidth))
	}

	panel := tui.RenderPanel("Agent Log", body, panelWidth, agentLogPanelTheme())
	return append([]string{""}, panel...)
}

// agentLogEntries resolves the ring's buffered entries into renderable
// tui.LogEntry values: classified entries use their authoritative severity;
// unclassified ones fall back to keyword classification of the text.
func (a *Agent) agentLogEntries() []tui.LogEntry {
	raw := a.logs.recentEntries()
	out := make([]tui.LogEntry, len(raw))
	for i, e := range raw {
		sev := e.sev
		if !e.classified {
			sev = agentLogSeverity(e.text)
		}
		out[i] = tui.LogEntry{Text: e.text, Severity: sev, Time: e.t}
	}
	return out
}

// agentLogPanelTheme matches the gateway log panel: muted section title and a
// gray border, with no body padding.
func agentLogPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleMutedSection, BorderStyle: tui.StyleGrayBorder}
}

// agentFormatLogLine styles one log line for the Agent Log panel, using the
// same glyphs and colors as the gateway Routing Log panel: green "•" for
// positive lifecycle events, yellow "!" for problems, dim "·" otherwise. The
// glyph/color rendering is shared via tui.FormatLogLine; only the keyword
// classification below is agent-specific. It is the fallback path for entries
// that arrive without an authoritative severity (e.g. raw io.Writer output).
func agentFormatLogLine(line string, contentWidth int) string {
	return tui.FormatLogLine(line, contentWidth, agentLogSeverity(line))
}

// agentLogSeverity maps an agent log line to its display severity by keyword.
func agentLogSeverity(line string) tui.LogSeverity {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(lower, "warning"):
		return tui.LogInfo
	case strings.Contains(lower, "fail") || strings.Contains(lower, "error") || strings.Contains(lower, "could not"):
		return tui.LogWarn
	case strings.Contains(lower, "complete") || strings.Contains(lower, "rotated") ||
		strings.Contains(lower, "started") || strings.Contains(lower, "cleared"):
		return tui.LogGood
	default:
		return tui.LogInfo
	}
}

// framePaint emits one full refresh without a screen-clearing blank frame.
// Each line is overwritten in place and cleared to EOL; only rows below a
// previously taller frame are cleared. No trailing newline, so the last row
// never forces a scroll.
func framePaint(panelLines []string) string {
	var b strings.Builder
	b.Grow(len(panelLines) * 96)
	b.WriteString("\x1b[?2026h") // begin synchronized update (no-op if unsupported)
	b.WriteString("\x1b[H")      // cursor home, NO full clear
	for i, ln := range panelLines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString(ln)
		b.WriteString("\x1b[K") // erase stale tail from a longer prior line
	}
	b.WriteString("\x1b[J")      // erase rows below a previously taller frame
	b.WriteString("\x1b[?2026l") // end synchronized update
	return b.String()
}

// agentRenewalCell formats a renewal timestamp with color coding based on
// the fraction of lifetime elapsed:
//
//	Green  : elapsed < 70% of lifetime (healthy)
//	Yellow : elapsed >= 70% but < 80% (approaching renewal)
//	Red    : elapsed >= 80% (at or past renewal threshold)
//
// The returned string is padded to width visible characters (before ANSI codes)
// so adjacent columns stay aligned.
func agentRenewalCell(now, issuedAt, notAfter time.Time) string {
	remaining := notAfter.Sub(now)
	label := agentFormatDuration(remaining)
	padded := tui.PadDisplay(label, colRenewalW)

	if remaining <= 0 {
		return "\x1b[31m" + padded + "\x1b[0m" // red — expired
	}

	lifetime := notAfter.Sub(issuedAt)
	if lifetime <= 0 || issuedAt.IsZero() {
		// Cannot compute fraction — fall back to green.
		return "\x1b[32m" + padded + "\x1b[0m"
	}

	elapsed := now.Sub(issuedAt)
	fraction := float64(elapsed) / float64(lifetime)

	switch {
	case fraction >= 0.80:
		return "\x1b[31m" + padded + "\x1b[0m" // red
	case fraction >= 0.70:
		return "\x1b[33m" + padded + "\x1b[0m" // yellow
	default:
		return "\x1b[32m" + padded + "\x1b[0m" // green
	}
}

// Calendar-scale display approximations for agentFormatDuration.  Months and
// years are not exact (real ones vary), but at these magnitudes a renewal
// countdown only needs a readable order-of-magnitude, so a fixed 30-day month
// and 365-day year are the conventional choice.
const (
	durDay   = 24 * time.Hour
	durMonth = 30 * durDay
	durYear  = 365 * durDay
)

// agentFormatDuration renders a remaining duration with granularity scaled to
// its magnitude, showing the two or three most significant units (akin to
// `systemctl`/`uptime` style countdowns).  Precision drops as the value grows,
// so large countdowns stay readable instead of trailing noise like "400d 5h 3m":
//
//	d < 0      → "needs renewal"
//	d < 1m     → seconds            e.g. "42s"
//	d < 1h     → minutes + seconds  e.g. "12m 5s"
//	d < 24h    → hours + minutes    e.g. "5h 20m"
//	d < 30d    → days + hours + min e.g. "2d 3h 15m"
//	d < 365d   → months + days      e.g. "3mo 12d"
//	d >= 365d  → years + days        e.g. "1y 35d"
func agentFormatDuration(d time.Duration) string {
	if d < 0 {
		return "needs renewal"
	}

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d/time.Minute), int(d/time.Second)%60)
	case d < durDay:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d/time.Minute)%60)
	case d < durMonth:
		return fmt.Sprintf("%dd %dh %dm", int(d/durDay), int(d/time.Hour)%24, int(d/time.Minute)%60)
	case d < durYear:
		return fmt.Sprintf("%dmo %dd", int(d/durMonth), int(d%durMonth/durDay))
	default:
		return fmt.Sprintf("%dy %dd", int(d/durYear), int(d%durYear/durDay))
	}
}

// agentExpirationCell returns the local-time expiration string for a given
// artifact:
//   - CRL: no expiry (shown as "–")
//   - PSK: uses the PSKRotate notAfter (100% mark = when the key expires)
//   - all others: uses the artifact's own notAfter
func agentExpirationCell(art ArtifactID, artifacts map[ArtifactID]time.Time) string {
	if art == ArtifactCRL {
		return tui.Dim(tui.PadDisplay("–", colExpirationW))
	}
	var expiry time.Time
	if art == ArtifactPSK {
		expiry = artifacts[ArtifactPSKRotate]
	} else {
		expiry = artifacts[art]
	}
	if expiry.IsZero() {
		return tui.Dim(tui.PadDisplay("–", colExpirationW))
	}
	return tui.PadDisplay(expiry.Local().Format("2006-01-02 15:04:05"), colExpirationW)
}

// keyKind identifies the type of keyboard event emitted by startKeyReader.
type keyKind int

const (
	keyStop       keyKind = iota // Ctrl+C — stop the agent
	keyAddProfile                // Ctrl+A — open enrollment wizard
	keyEnter                     // Enter — confirm (renew row, or activate button)
	keyTab                       // Tab — cycle to next profile (wraps)
	keyLeft                      // Left arrow — focus Stop button
	keyRight                     // Right arrow — focus Add profile button
	keyRowUp                     // Up arrow — move row cursor up
	keyRowDown                   // Down arrow — move row cursor down
	keyJumpTab                   // digit 1–9; num = 0-based target tab index
)

// keyEvent is a parsed keyboard event from the TUI.
type keyEvent struct {
	kind keyKind
	num  int // used by keyJumpTab; zero for all other kinds
}

// startKeyReader puts inFile in raw mode and spawns a goroutine that sends
// recognised key events to the returned channel.  It returns a stop func that
// restores the terminal and waits for the goroutine to exit; the caller must
// invoke stop before reading from inFile (e.g. for the enrollment wizard).
func startKeyReader(ctx context.Context, inFile *os.File, oldState *term.State) (<-chan keyEvent, func()) {
	ch := make(chan keyEvent, 4)

	// Wakeup pipe: writing to pw causes the goroutine's poll to return so it
	// exits cleanly without needing an additional keypress from the user.
	pr, pw, err := os.Pipe()
	if err != nil {
		close(ch)
		return ch, func() {}
	}

	stdinFd := int(inFile.Fd())

	send := func(k keyEvent) bool {
		select {
		case ch <- k:
			return true
		case <-ctx.Done():
			return false
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16)
		pollFds := []unix.PollFd{
			{Fd: int32(stdinFd), Events: unix.POLLIN},
			{Fd: int32(pr.Fd()), Events: unix.POLLIN},
		}
		for {
			_, err := unix.Poll(pollFds, -1)
			if err != nil {
				if err == syscall.EINTR {
					continue
				}
				return
			}
			// Wakeup pipe has data (or was closed) — time to stop.
			if pollFds[1].Revents != 0 {
				return
			}
			if pollFds[0].Revents&unix.POLLIN == 0 {
				continue
			}
			n, rerr := unix.Read(stdinFd, buf)
			if rerr != nil || n == 0 {
				return
			}
			i := 0
			for i < n {
				switch buf[i] {
				case 0x03: // Ctrl+C
					i++
					if !send(keyEvent{kind: keyStop}) {
						return
					}
				case 0x01: // Ctrl+A
					i++
					if !send(keyEvent{kind: keyAddProfile}) {
						return
					}
				case 0x09: // Tab → cycle profile
					i++
					if !send(keyEvent{kind: keyTab}) {
						return
					}
				case 0x0d, 0x0a: // Enter (CR or LF) → confirm
					i++
					if !send(keyEvent{kind: keyEnter}) {
						return
					}
				case 0x1b: // Escape — could be start of arrow-key sequence ESC [ X
					if i+2 < n && buf[i+1] == '[' {
						switch buf[i+2] {
						case 'A': // Up arrow
							i += 3
							if !send(keyEvent{kind: keyRowUp}) {
								return
							}
						case 'B': // Down arrow
							i += 3
							if !send(keyEvent{kind: keyRowDown}) {
								return
							}
						case 'D': // Left arrow
							i += 3
							if !send(keyEvent{kind: keyLeft}) {
								return
							}
						case 'C': // Right arrow
							i += 3
							if !send(keyEvent{kind: keyRight}) {
								return
							}
						default:
							i += 3 // skip unknown sequence
						}
					} else {
						i++ // lone ESC or incomplete — skip
					}
				default:
					if buf[i] >= '1' && buf[i] <= '9' {
						tab := int(buf[i] - '1')
						i++
						if !send(keyEvent{kind: keyJumpTab, num: tab}) {
							return
						}
					} else {
						i++ // unrecognised byte — skip
					}
				}
			}
		}
	}()

	stop := func() {
		// Signal the goroutine to exit by writing to the wakeup pipe, then wait.
		_, _ = pw.Write([]byte{0})
		pw.Close()
		<-done
		pr.Close()
		// Restore cooked mode after the goroutine has exited so the wizard
		// (or any subsequent reader) gets line-buffered canonical input.
		if oldState != nil {
			_ = term.Restore(stdinFd, oldState)
		}
	}
	return ch, stop
}

// runDisplay renders the live TUI when stdout is a terminal, or emits plain
// log-line output otherwise (pipe / systemd / non-interactive).
// Blocks until ctx is cancelled.
func (a *Agent) runDisplay(ctx context.Context) {
	tw := a.termOut
	if tw == nil {
		tw = a.Out
	}
	if agentIsTerminal(tw) {
		// The live TUI owns the terminal: emit() must route events to the file
		// sink and ring, not stdout, so it does not corrupt the rendered frame.
		a.tuiActive.Store(true)
		defer a.tuiActive.Store(false)

		// Hide cursor while rendering.
		_, _ = io.WriteString(tw, "\x1b[?25l")
		defer func() { _, _ = io.WriteString(tw, "\x1b[?25h") }()

		// Set up keyboard handling when stdin is a real terminal.
		var keyCh <-chan keyEvent
		var stopKeys func()
		if inFile, ok := a.In.(*os.File); ok && term.IsTerminal(int(inFile.Fd())) {
			oldState, err := term.MakeRaw(int(inFile.Fd()))
			if err == nil {
				keyCh, stopKeys = startKeyReader(ctx, inFile, oldState)
				defer func() {
					if stopKeys != nil {
						stopKeys()
					}
				}()
			}
		}
		if keyCh == nil {
			keyCh = make(chan keyEvent) // never fires — fallback
		}

		vs := agentViewState{}
		repaint := func() { _, _ = io.WriteString(tw, a.renderAgentView(vs)) }

		clamp := func() {
			profs := a.snapshotProfiles()
			if len(profs) == 0 {
				vs.activeTab = 0
				vs.rowSel = 0
				return
			}
			if vs.activeTab >= len(profs) {
				vs.activeTab = len(profs) - 1
			}
			if vs.activeTab < 0 {
				vs.activeTab = 0
			}
			if vs.rowSel >= len(displayArtifacts) {
				vs.rowSel = len(displayArtifacts) - 1
			}
			if vs.rowSel < 0 {
				vs.rowSel = 0
			}
		}

		renew := func() {
			profs := a.snapshotProfiles()
			if vs.activeTab >= len(profs) {
				return
			}
			if vs.rowSel >= len(displayArtifacts) {
				return
			}
			p := profs[vs.activeTab]
			art := displayArtifacts[vs.rowSel]
			if err := a.RenewArtifact(p.domainTemplateID, p.participantID, p.serial, art); err != nil {
				a.emitf(catRenewal, tui.LogWarn, "manual renew %s failed: %v", artifactLabel[art], err)
			} else {
				a.emitf(catRenewal, tui.LogGood, "manual renew %s requested", artifactLabel[art])
			}
		}

		addProfile := func() {
			if stopKeys != nil {
				stopKeys()
				stopKeys = nil
			}
			_, _ = io.WriteString(tw, "\x1b[?25h\x1b[H\x1b[J")
			if err := a.ConfigureFirstRun(ctx); err != nil && ctx.Err() == nil {
				_, _ = fmt.Fprintf(tw, "\r\nError: %v\r\n", err)
				time.Sleep(2 * time.Second)
			}
			if ctx.Err() != nil {
				return
			}
			if inFile, ok := a.In.(*os.File); ok && term.IsTerminal(int(inFile.Fd())) {
				if oldState, err := term.MakeRaw(int(inFile.Fd())); err == nil {
					keyCh, stopKeys = startKeyReader(ctx, inFile, oldState)
				}
			}
			_, _ = io.WriteString(tw, "\x1b[?25l")
			vs.focus = focusTable
			clamp()
			repaint()
		}

		// Clear the screen once before the first render so that the orange
		// panel border is not obscured by earlier terminal output.
		_, _ = io.WriteString(tw, "\x1b[H\x1b[J")
		repaint()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_, _ = io.WriteString(tw, a.renderAgentView(vs))
				return
			case <-ticker.C:
				repaint()
			case ev := <-keyCh:
				switch ev.kind {
				case keyStop:
					if a.stopFunc != nil {
						a.stopFunc()
					}
				case keyAddProfile:
					addProfile()
				case keyTab:
					profs := a.snapshotProfiles()
					if len(profs) > 0 {
						vs.activeTab = (vs.activeTab + 1) % len(profs)
						vs.rowSel = 0
						vs.focus = focusTable
						repaint()
					}
				case keyLeft:
					vs.focus = focusButtons
					vs.btnSel = btnStop
					repaint()
				case keyRight:
					vs.focus = focusButtons
					vs.btnSel = btnAdd
					repaint()
				case keyRowUp:
					vs.focus = focusTable
					if vs.rowSel > 0 {
						vs.rowSel--
					}
					repaint()
				case keyRowDown:
					vs.focus = focusTable
					if vs.rowSel < len(displayArtifacts)-1 {
						vs.rowSel++
					}
					repaint()
				case keyEnter:
					if vs.focus == focusButtons {
						if vs.btnSel == btnStop {
							if a.stopFunc != nil {
								a.stopFunc()
							}
						} else {
							addProfile()
						}
					} else {
						renew()
						repaint()
					}
				case keyJumpTab:
					profs := a.snapshotProfiles()
					if ev.num >= 0 && ev.num < len(profs) {
						vs.activeTab = ev.num
						vs.rowSel = 0
						vs.focus = focusTable
						repaint()
					}
				}
			}
		}
	} else {
		a.emitf(catState, tui.LogInfo, "agent started inbox=%s", a.InboxDir)
		<-ctx.Done()
	}
}

func agentIsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
