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
	return p.serviceID + "/" + p.participantID + "/" + p.deviceName
}

func (a *Agent) snapshotProfiles() []profileSnapshot {
	var out []profileSnapshot
	a.profiles.Range(func(_, val any) bool {
		p := val.(*profile)
		p.mu.Lock()
		na := make(map[ArtifactID]time.Time, len(p.notAfter))
		for k, v := range p.notAfter {
			na[k] = v
		}
		is := make(map[ArtifactID]time.Time, len(p.issuedAt))
		for k, v := range p.issuedAt {
			is[k] = v
		}
		out = append(out, profileSnapshot{p.serial, p.serviceID, p.effectiveDomainID(), p.participantID, p.deviceName, p.state, na, is})
		p.mu.Unlock()
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// agentViewState holds the mutable TUI navigation state for runDisplay.
type agentViewState struct {
	activeTab   int
	rowSel      int
	focus       focusZone
	btnSel      int
	status      string
	statusUntil time.Time
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
		if i == active {
			label := profiles[i].deviceName
			if label == "" {
				label = profiles[i].participantID
			}
			label = truncateLabel(label, tabActiveLabelMax)
			chips = append(chips, orangeFg+"\x1b[1m[ "+num+" "+label+" ]\x1b[0m")
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
	width, _ := tui.TerminalSize(tw, agentDefaultWidth, agentDefaultHeight)
	contentWidth := tui.MaxInt(60, width-4)

	countLine := fmt.Sprintf("%d participant artifacts monitored  •  Logs: %s", len(profiles), a.LogFile)
	body := []string{tui.PadStyled(countLine, contentWidth)}

	// Transient status line (shown for 4 s after a renew action).
	if vs.status != "" && now.Before(vs.statusUntil) {
		body = append(body, tui.Dim("  "+vs.status))
	}

	if len(profiles) == 0 {
		body = append(body, "")
		body = append(body, tui.Dim("  (waiting for enrollment — see hint below)"))
	} else {
		body = append(body, "")
		body = append(body, renderTabBar(profiles, vs.activeTab))
		body = append(body, "")

		pr := profiles[vs.activeTab]

		// ── Identifiers section ──
		body = append(body, tui.StyleSection("Identifiers"))
		body = append(body, tui.Dim("  Serial:               ")+pr.serial)
		body = append(body, tui.Dim("  Edge Provision Svc:   ")+pr.serviceID)
		body = append(body, tui.Dim("  Domain Template:      ")+pr.domainTemplateID)
		body = append(body, tui.Dim("  Participant Template: ")+pr.participantID)
		body = append(body, "")

		// ── Artifact renewal table ──
		artHeader := "  " + tui.StyleLabel("ARTIFACT", colArtifactW) + "  " +
			tui.StyleLabel("RENEWS IN", colRenewalW) + "  " +
			tui.StyleLabel("EXPIRES AT", colExpirationW)
		artSep := "  " + tui.Dim(strings.Repeat("─", colArtifactW+2+colRenewalW+2+colExpirationW))
		body = append(body, artHeader, artSep)

		for rowIdx, art := range displayArtifacts {
			na, ok := pr.notAfter[art]
			var renewCell string
			if !ok || na.IsZero() {
				renewCell = tui.Dim(tui.PadDisplay("–", colRenewalW))
			} else {
				renewCell = agentRenewalCell(now, pr.issuedAt[art], na)
			}
			expiryCell := agentExpirationCell(art, pr.notAfter)
			artName := tui.PadDisplay(artifactLabel[art], colArtifactW)
			cells := artName + "  " + renewCell + "  " + expiryCell

			switch {
			case rowIdx == vs.rowSel && vs.focus == focusTable:
				// Full-line background highlight. Each embedded \x1b[0m inside
				// the colored cells is rewritten to also re-apply the row
				// background, so the bar does not break at the first reset.
				inner := strings.ReplaceAll("  "+cells, "\x1b[0m", "\x1b[0m"+rowHighlightBg)
				body = append(body, rowHighlightBg+inner+"  \x1b[1mPress Enter to renew\x1b[0m")
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

	theme := tui.PanelTheme{
		TitleStyle:  tui.StyleTitle,
		BorderStyle: tui.StyleOrangeBorder,
		PaddedBody:  true,
	}
	panelLines := tui.RenderPanel("Edge-Sync Agent", body, width, theme)
	return framePaint(panelLines)
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

func agentFormatDuration(d time.Duration) string {
	if d < 0 {
		return "needs renewal"
	}
	if d < time.Minute {
		return "<1m"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
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
			if err := a.RenewArtifact(p.domainTemplateID, p.participantID, p.deviceName, art); err != nil {
				vs.status = fmt.Sprintf("renew %s failed: %v", artifactLabel[art], err)
			} else {
				vs.status = fmt.Sprintf("renew %s requested", artifactLabel[art])
			}
			vs.statusUntil = a.Now().Add(4 * time.Second)
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
		_, _ = fmt.Fprintf(a.Out, "agent started inbox=%s\n", a.InboxDir)
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
