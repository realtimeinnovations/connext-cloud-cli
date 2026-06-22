package edgesyncagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
	"golang.org/x/term"
)

// ParseCampaignToken decodes the JWT payload (without signature verification)
// and extracts the service_id and participant_id claims.
func ParseCampaignToken(token string) (serviceID, participantID string, err error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return "", "", fmt.Errorf("not a valid JWT (expected 3 dot-separated parts)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("invalid JWT payload encoding: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", fmt.Errorf("invalid JWT payload JSON: %w", err)
	}
	// Look for edge_system_id / participant_id under both plain keys and
	// namespaced URIs (e.g. "https://devices.cloud.rti.com/edge_system_id").
	serviceID = claimValue(claims, "edge_system_id", "service_id")
	participantID = claimValue(claims, "participant_id")
	if serviceID == "" || participantID == "" {
		formatted, _ := json.MarshalIndent(claims, "", "  ")
		return "", "", fmt.Errorf(
			"could not find edge_system_id/participant_id in token claims.\nDecoded payload:\n%s",
			formatted,
		)
	}
	return serviceID, participantID, nil
}

// CampaignTokenDeviceDomain decodes the JWT payload (without signature
// verification) and extracts the device_domain claim.  Returns an empty string
// if the token is invalid or the claim is absent.
func CampaignTokenDeviceDomain(token string) string {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claimValue(claims, "device_domain")
}

// claimValue returns the first non-empty string value for a claim key,
// checking both plain and URI-namespaced variants.
func claimValue(claims map[string]any, keys ...string) string {
	for _, k := range keys {
		// Plain key first.
		if v, _ := claims[k].(string); v != "" {
			return v
		}
		// Then scan for any namespaced key ending in /<key>.
		suffix := "/" + k
		for fullKey, raw := range claims {
			if strings.HasSuffix(fullKey, suffix) {
				if v, _ := raw.(string); v != "" {
					return v
				}
			}
		}
	}
	return ""
}

// DetectSerial returns a stable hardware identifier for the current machine:
// /etc/machine-id when available, falling back to the system hostname.
func DetectSerial() string {
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return s
		}
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		return host
	}
	return ""
}

// DetectMACs returns the hardware MAC addresses of all non-loopback network
// interfaces that have a non-empty hardware address.
func DetectMACs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var macs []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		macs = append(macs, iface.HardwareAddr.String())
	}
	return macs
}

// ConfigureFirstRun runs the interactive enrollment wizard.
// It blocks until a successful enrollment or an error/cancellation.
func (a *Agent) ConfigureFirstRun(ctx context.Context) error {
	_, _ = fmt.Fprint(a.promptOut(), renderWizardIntro())

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// ── 1. Campaign token ────────────────────────────────────────────────
		token, err := a.readTokenInput("Campaign token")
		if err != nil {
			return err
		}
		token = strings.TrimSpace(token)
		if token == "" {
			_, _ = fmt.Fprintln(a.Out, "Error: campaign token is required")
			_, _ = fmt.Fprintln(a.Out)
			continue
		}

		// Show a truncated confirmation so the user sees what was entered
		// without flooding the terminal with the full JWT.
		_, _ = fmt.Fprintf(a.promptOut(), "\x1b[38;5;208m✔\x1b[0m Campaign token: %s\n", truncateToken(token, 60))

		serviceID, participantID, err := ParseCampaignToken(token)
		if err != nil {
			_, _ = fmt.Fprintf(a.Out, "Error: %v\n\n", err)
			choice, err2 := a.SelectFunc("What would you like to do?", []string{"try again", "exit"})
			if err2 != nil || choice == "exit" {
				if err2 != nil {
					return err2
				}
				return fmt.Errorf("enrollment cancelled")
			}
			continue
		}
		_, _ = fmt.Fprintf(a.Out, "  Service ID:     %s\n", serviceID)
		_, _ = fmt.Fprintf(a.Out, "  Participant ID: %s\n", participantID)
		_, _ = fmt.Fprintln(a.Out)

		// ── 2. Serial number ─────────────────────────────────────────────────
		detectedSerial := DetectSerial()
		var serial string
		if a.ManualMode {
			if detectedSerial != "" {
				choice, err := a.SelectFunc(
					fmt.Sprintf("Serial number (detected: %s)", detectedSerial),
					[]string{detectedSerial, "enter manually"},
				)
				if err != nil {
					return err
				}
				if choice == "enter manually" {
					serial, err = a.InputFunc("Serial number")
					if err != nil {
						return err
					}
				} else {
					serial = choice
				}
			} else {
				serial, err = a.InputFunc("Serial number")
				if err != nil {
					return err
				}
			}
		} else {
			if detectedSerial != "" {
				serial = detectedSerial
				_, _ = fmt.Fprintf(a.promptOut(), "\x1b[38;5;208m✔\x1b[0m Serial number: %s\n", serial)
			} else {
				serial, err = a.InputFunc("Serial number")
				if err != nil {
					return err
				}
			}
		}
		serial = strings.TrimSpace(serial)
		if serial == "" {
			_, _ = fmt.Fprintln(a.Out, "Error: serial number is required")
			_, _ = fmt.Fprintln(a.Out)
			continue
		}

		// ── 4. MAC address(es) ───────────────────────────────────────────────
		detectedMACs := DetectMACs()
		var macs []string
		if a.ManualMode {
			if len(detectedMACs) > 0 {
				choice, err := a.SelectFunc(
					"MAC addresses:",
					[]string{
						fmt.Sprintf("use all detected (%d addresses)", len(detectedMACs)),
						"enter manually",
					},
				)
				if err != nil {
					return err
				}
				if choice == "enter manually" {
					macs, err = a.promptMultipleMACs()
					if err != nil {
						return err
					}
				} else {
					macs = detectedMACs
				}
			} else {
				macs, err = a.promptMultipleMACs()
				if err != nil {
					return err
				}
			}
		} else {
			if len(detectedMACs) > 0 {
				macs = detectedMACs
				_, _ = fmt.Fprintf(a.promptOut(), "\x1b[38;5;208m✔\x1b[0m MAC addresses: %s\n", strings.Join(macs, ", "))
			} else {
				macs, err = a.promptMultipleMACs()
				if err != nil {
					return err
				}
			}
		}

		if len(macs) == 0 {
			_, _ = fmt.Fprintln(a.Out, "Error: at least one MAC address is required")
			_, _ = fmt.Fprintln(a.Out)
			continue
		}

		// ── 5. Confirm and enroll ─────────────────────────────────────────────
		_, _ = fmt.Fprintln(a.Out)
		_, _ = fmt.Fprintln(a.Out, "Enrolling:")
		_, _ = fmt.Fprintf(a.Out, "  Service:     %s\n", serviceID)
		_, _ = fmt.Fprintf(a.Out, "  Participant: %s\n", participantID)
		_, _ = fmt.Fprintf(a.Out, "  Serial:      %s\n", serial)
		_, _ = fmt.Fprintf(a.Out, "  MACs:        %s\n", strings.Join(macs, ", "))
		_, _ = fmt.Fprintln(a.Out)

		req := EnrollRequest{
			ServiceID:     serviceID,
			ParticipantID: participantID,
			CampaignToken: token,
			Serial:        serial,
			MACs:          macs,
		}
		if err := a.enrollProfile(req); err != nil {
			_, _ = fmt.Fprintf(a.Out, "Enrollment failed: %v\n\n", err)
			choice, err2 := a.SelectFunc("What would you like to do?", []string{"try again", "exit"})
			if err2 != nil || choice == "exit" {
				if err2 != nil {
					return err2
				}
				return fmt.Errorf("enrollment failed: %w", err)
			}
			continue
		}

		_, _ = fmt.Fprintln(a.Out, "Enrolled successfully.  Starting agent...")
		_, _ = fmt.Fprintln(a.Out)
		return nil
	}
}

// promptMultipleMACs collects MAC addresses from a single comma-separated input.
func (a *Agent) promptMultipleMACs() ([]string, error) {
	input, err := a.InputFunc("MAC addresses (comma-separated, e.g. AA:BB:CC:DD:EE:11, 11:22:33:44:55:11)")
	if err != nil {
		return nil, err
	}
	var macs []string
	for _, part := range strings.Split(input, ",") {
		mac := strings.TrimSpace(part)
		if mac != "" {
			macs = append(macs, mac)
		}
	}
	return macs, nil
}

// readTokenInput reads a campaign token without echoing the input to the
// terminal.  Long JWTs cause promptui to corrupt the display when pasted,
// so we bypass it entirely and use term.ReadPassword (hidden input).
func (a *Agent) readTokenInput(label string) (string, error) {
	_, _ = fmt.Fprintf(a.promptOut(), "%s (input hidden): ", label)

	if f, ok := a.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		_, _ = fmt.Fprintln(a.promptOut()) // newline after hidden input
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}

	// Non-terminal (pipe / test): read one line without extra buffering.
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := a.In.Read(buf)
		if n > 0 && buf[0] != '\n' {
			line = append(line, buf[0])
		}
		if (n > 0 && buf[0] == '\n') || err != nil {
			break
		}
	}
	return strings.TrimSpace(string(line)), nil
}

// truncateToken returns a shortened display of the JWT for the confirmation line.
func truncateToken(token string, maxLen int) string {
	if len(token) <= maxLen {
		return token
	}
	return token[:maxLen] + "…"
}

// renderWizardIntro builds a styled TUI panel for the wizard intro,
// matching the agent's RenderPanel style.
func renderWizardIntro() string {
	body := []string{
		tui.Dim("No enrolled profiles found — starting enrollment wizard."),
		"",
		tui.Dim("Paste the campaign token when prompted (input will be hidden)."),
		tui.Dim("You can add more profiles later with: rticloud edge-sync agent enroll"),
	}
	// Compute width from widest body line + panel border padding (4 chars).
	width := 0
	for _, line := range body {
		if w := tui.DisplayWidth(line); w > width {
			width = w
		}
	}
	width += 4
	theme := tui.PanelTheme{
		TitleStyle:  tui.StyleTitle,
		BorderStyle: tui.StyleOrangeBorder,
		PaddedBody:  true,
	}
	lines := tui.RenderPanel("Edge-Sync Agent setup", body, width, theme)
	return "\n" + strings.Join(lines, "\n") + "\n"
}
