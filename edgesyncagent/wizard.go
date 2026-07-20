// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// Choice labels for the first-run enrollment mode question.
const (
	enrollChoiceReuse    = "Reuse an enrollment already on this device"
	enrollChoiceOperator = "My Connext Cloud account (operator login)"
	enrollChoiceCampaign = "A campaign token (issued by an operator)"
)

// ConfigureFirstRun runs first-time enrollment.  It blocks until a successful
// enrollment or an error/cancellation.
//
// Interactive by default: the user chooses between operator (direct)
// enrollment — picking the Provisioning Service and templates from the
// account catalogue — and campaign enrollment (pasting a campaign token).
// When the enrollment is fully specified up front (CampaignToken, or Service +
// DomainTemplateID + ParticipantTemplateID) it runs headless with no prompts.
func (a *Agent) ConfigureFirstRun(ctx context.Context) error {
	if a.CampaignToken != "" {
		return a.enrollHeadlessCampaign()
	}
	if a.Service != "" && a.DomainTemplateID != "" && a.ParticipantTemplateID != "" {
		return a.enrollHeadlessDirect()
	}

	// Offer to reuse an enrollment already present on disk (performed
	// out-of-band by `rticloud edge-provisioning enroll`/`enroll-direct`)
	// before falling back to a fresh enrollment.
	adoptable := a.findAdoptableNodes()

	_, _ = fmt.Fprint(a.promptOut(), renderWizardIntro())

	choices := []string{enrollChoiceOperator, enrollChoiceCampaign}
	if len(adoptable) > 0 {
		choices = append([]string{enrollChoiceReuse}, choices...)
	}

	mode, err := a.SelectFunc("How do you want to enroll this device?", choices)
	if err != nil {
		return err
	}
	switch mode {
	case enrollChoiceReuse:
		return a.reuseWizard(ctx, adoptable)
	case enrollChoiceCampaign:
		return a.campaignWizard(ctx)
	default:
		return a.operatorWizard(ctx)
	}
}

// reuseWizard adopts one or more enrollments already present on disk instead of
// enrolling a new device.  The user picks a single discovered enrollment, or
// "all detected enrollments" to adopt every one at once (auto-selected when only
// one exists); adoption runs the artifact-fetch sequence against the stored
// mTLS credentials.  Adoption is best-effort across the selection: the agent
// proceeds as long as at least one succeeds, and only offers retry/exit when
// none could be reused.
func (a *Agent) reuseWizard(ctx context.Context, adoptable []adoptableNode) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		chosen, err := a.chooseAdoptable(adoptable)
		if err != nil {
			return err
		}

		a.printReuseSummary(chosen)

		adopted := 0
		var lastErr error
		for _, n := range chosen {
			if err := a.adoptProfile(n); err != nil {
				_, _ = fmt.Fprintf(a.Out, "  \x1b[31m✗\x1b[0m %s: %v\n", adoptableLabel(n), err)
				lastErr = err
				continue
			}
			adopted++
		}

		// Nothing adopted — treat like a failed enrollment: retry or exit.
		if adopted == 0 {
			retry, err2 := a.retryOrExit(fmt.Errorf("reuse failed: %w", lastErr))
			if !retry {
				return err2
			}
			continue
		}

		if lastErr != nil {
			_, _ = fmt.Fprintf(a.Out, "Reused %d of %d enrollments; the rest were skipped.\n", adopted, len(chosen))
		} else if adopted == 1 {
			_, _ = fmt.Fprintln(a.Out, "Enrollment reused successfully.  Starting agent...")
		} else {
			_, _ = fmt.Fprintf(a.Out, "%d enrollments reused successfully.  Starting agent...\n", adopted)
		}
		_, _ = fmt.Fprintln(a.Out)
		return nil
	}
}

// printReuseSummary lists the enrollment(s) about to be reused.
func (a *Agent) printReuseSummary(chosen []adoptableNode) {
	_, _ = fmt.Fprintln(a.Out)
	if len(chosen) == 1 {
		n := chosen[0]
		_, _ = fmt.Fprintln(a.Out, "Reusing existing enrollment:")
		_, _ = fmt.Fprintf(a.Out, "  Service:         %s\n", n.service)
		_, _ = fmt.Fprintf(a.Out, "  Domain:          %s\n", n.domain)
		_, _ = fmt.Fprintf(a.Out, "  Participant:     %s\n", n.participant)
		_, _ = fmt.Fprintf(a.Out, "  Deployment name: %s\n", n.node)
		_, _ = fmt.Fprintln(a.Out)
		return
	}
	_, _ = fmt.Fprintf(a.Out, "Reusing %d existing enrollments:\n", len(chosen))
	for _, n := range chosen {
		_, _ = fmt.Fprintf(a.Out, "  • %s\n", adoptableLabel(n))
	}
	_, _ = fmt.Fprintln(a.Out)
}

// chooseAdoptable returns the enrollment(s) to reuse.  A single candidate is
// announced and returned directly.  With multiple candidates the user is
// offered a pick-list whose first entry adopts every detected enrollment at
// once, followed by each individual enrollment.
func (a *Agent) chooseAdoptable(adoptable []adoptableNode) ([]adoptableNode, error) {
	if len(adoptable) == 1 {
		n := adoptable[0]
		_, _ = fmt.Fprintf(a.promptOut(), "\x1b[32m✓\x1b[0m Enrollment: %s\n", adoptableLabel(n))
		return adoptable, nil
	}
	allLabel := fmt.Sprintf("Use all %d detected enrollments", len(adoptable))
	labels := make([]string, 0, len(adoptable)+1)
	labels = append(labels, allLabel)
	byLabel := make(map[string]adoptableNode, len(adoptable))
	for _, n := range adoptable {
		l := adoptableLabel(n)
		labels = append(labels, l)
		byLabel[l] = n
	}
	choice, err := a.SelectFunc("Select the enrollment(s) to reuse:", labels)
	if err != nil {
		return nil, err
	}
	if choice == allLabel {
		return adoptable, nil
	}
	return []adoptableNode{byLabel[choice]}, nil
}

// adoptableLabel renders a discovered enrollment for the reuse pick-list.
func adoptableLabel(n adoptableNode) string {
	return fmt.Sprintf("%s / %s / %s (serial %s)", n.service, n.domain, n.participant, n.node)
}

// campaignWizard drives campaign enrollment: the user pastes a campaign token
// issued by an operator and the device enrolls against the campaign endpoint.
func (a *Agent) campaignWizard(ctx context.Context) error {
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
		_, _ = fmt.Fprintf(a.promptOut(), "\x1b[32m✓\x1b[0m Campaign token: %s\n", truncateToken(token, 60))

		serviceID, participantID, err := ParseCampaignToken(token)
		if err != nil {
			retry, err2 := a.retryOrExit(err)
			if !retry {
				return err2
			}
			continue
		}
		_, _ = fmt.Fprintf(a.Out, "  Service ID:     %s\n", serviceID)
		_, _ = fmt.Fprintf(a.Out, "  Participant ID: %s\n", participantID)
		_, _ = fmt.Fprintln(a.Out)

		// ── 2. Serial number and MAC address(es) ─────────────────────────────
		serial, err := a.resolveSerial()
		if err != nil {
			return err
		}
		macs, err := a.resolveMACs(true)
		if err != nil {
			return err
		}

		// ── 3. Confirm and enroll ────────────────────────────────────────────
		req := EnrollRequest{
			ServiceID:     serviceID,
			ParticipantID: participantID,
			CampaignToken: token,
			Serial:        serial,
			MACs:          macs,
		}
		a.printEnrollSummary(req)
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

// wizardRetryError marks a failure the user may retry from the wizard loop
// (catalogue fetches, empty catalogues).  Prompt cancellations are returned
// unwrapped and abort the wizard immediately.
type wizardRetryError struct{ err error }

func (e wizardRetryError) Error() string { return e.err.Error() }
func (e wizardRetryError) Unwrap() error { return e.err }

// operatorWizard drives operator-initiated (direct) enrollment: the user
// picks the Provisioning Service, Domain Template and Participant Template
// from the account catalogue instead of pasting a campaign token.  Values
// pre-set via flags skip their pick-list; single-entry catalogues are
// selected automatically.  The catalogue calls require a management login and
// trigger the login flow when no session exists.
func (a *Agent) operatorWizard(ctx context.Context) error {
	if a.ListServicesFunc == nil || a.ListDomainTemplatesFunc == nil ||
		a.ListParticipantTemplatesFunc == nil {
		return fmt.Errorf("operator enrollment is not configured")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, err := a.buildDirectRequest()
		if err != nil {
			var retryable wizardRetryError
			if !errors.As(err, &retryable) {
				return err
			}
			retry, err2 := a.retryOrExit(err)
			if !retry {
				return err2
			}
			continue
		}

		a.printEnrollSummary(req)
		if err := a.enrollProfile(req); err != nil {
			retry, err2 := a.retryOrExit(fmt.Errorf("enrollment failed: %w", err))
			if !retry {
				return err2
			}
			continue
		}

		_, _ = fmt.Fprintln(a.Out, "Enrolled successfully.  Starting agent...")
		_, _ = fmt.Fprintln(a.Out)
		return nil
	}
}

// buildDirectRequest collects the direct-enrollment slot interactively:
// service, domain template and participant template come from the account
// catalogue; serial and MACs are auto-detected like the campaign flow.
func (a *Agent) buildDirectRequest() (EnrollRequest, error) {
	service, err := a.chooseCatalogItem("Provisioning Service", a.Service,
		a.ListServicesFunc,
		"rticloud edge-provisioning service create --name <name>")
	if err != nil {
		return EnrollRequest{}, err
	}
	domain, err := a.chooseCatalogItem("Domain Template", a.DomainTemplateID,
		func() ([]string, error) { return a.ListDomainTemplatesFunc(service) },
		fmt.Sprintf("rticloud edge-provisioning domain-template create --service %s ...", service))
	if err != nil {
		return EnrollRequest{}, err
	}
	participant, err := a.chooseCatalogItem("Participant Template", a.ParticipantTemplateID,
		func() ([]string, error) { return a.ListParticipantTemplatesFunc(service) },
		fmt.Sprintf("rticloud edge-provisioning participant-template create --service %s ...", service))
	if err != nil {
		return EnrollRequest{}, err
	}
	serial, err := a.resolveSerial()
	if err != nil {
		return EnrollRequest{}, err
	}
	macs, err := a.resolveMACs(false)
	if err != nil {
		return EnrollRequest{}, err
	}
	return EnrollRequest{
		ServiceID:        service,
		DomainTemplateID: domain,
		ParticipantID:    participant,
		Serial:           serial,
		MACs:             macs,
	}, nil
}

// chooseCatalogItem resolves one slot identifier for direct enrollment.
// Precedence: the pre-set flag value, the single catalogue entry (announced,
// not prompted), then an interactive selection.  An empty catalogue is a
// retryable error pointing at the CLI command that creates the resource.
func (a *Agent) chooseCatalogItem(label, preset string, list func() ([]string, error), createHint string) (string, error) {
	if preset != "" {
		_, _ = fmt.Fprintf(a.promptOut(), "\x1b[32m✓\x1b[0m %s: %s\n", label, preset)
		return preset, nil
	}
	items, err := list()
	if err != nil {
		return "", wizardRetryError{fmt.Errorf("listing %ss: %w", strings.ToLower(label), err)}
	}
	if len(items) == 0 {
		return "", wizardRetryError{fmt.Errorf("no %ss found; create one with:\n  %s", strings.ToLower(label), createHint)}
	}
	if len(items) == 1 {
		_, _ = fmt.Fprintf(a.promptOut(), "\x1b[32m✓\x1b[0m %s: %s\n", label, items[0])
		return items[0], nil
	}
	return a.SelectFunc(fmt.Sprintf("Select %s:", label), items)
}

// enrollHeadlessCampaign enrolls using the pre-supplied campaign token with no
// prompting (--campaign-token).
func (a *Agent) enrollHeadlessCampaign() error {
	serviceID, participantID, err := ParseCampaignToken(a.CampaignToken)
	if err != nil {
		return fmt.Errorf("invalid campaign token: %w", err)
	}
	serial, macs, err := a.headlessSerialAndMACs(true)
	if err != nil {
		return err
	}
	req := EnrollRequest{
		ServiceID:     serviceID,
		ParticipantID: participantID,
		CampaignToken: a.CampaignToken,
		Serial:        serial,
		MACs:          macs,
	}
	return a.enrollHeadless(req)
}

// enrollHeadlessDirect enrolls directly using the pre-supplied service,
// domain template and participant template with no prompting (requires a
// logged-in management token).
func (a *Agent) enrollHeadlessDirect() error {
	serial, macs, err := a.headlessSerialAndMACs(false)
	if err != nil {
		return err
	}
	req := EnrollRequest{
		ServiceID:        a.Service,
		DomainTemplateID: a.DomainTemplateID,
		ParticipantID:    a.ParticipantTemplateID,
		Serial:           serial,
		MACs:             macs,
	}
	return a.enrollHeadless(req)
}

// enrollHeadless runs a single non-interactive enrollment attempt.
func (a *Agent) enrollHeadless(req EnrollRequest) error {
	a.printEnrollSummary(req)
	if err := a.enrollProfile(req); err != nil {
		return fmt.Errorf("enrollment failed: %w", err)
	}
	_, _ = fmt.Fprintln(a.Out, "Enrolled successfully.  Starting agent...")
	_, _ = fmt.Fprintln(a.Out)
	return nil
}

// headlessSerialAndMACs resolves the serial and MAC addresses without
// prompting, from the DeploymentName/MACs flags first and auto-detection second.
func (a *Agent) headlessSerialAndMACs(macsRequired bool) (string, []string, error) {
	serial := a.DeploymentName
	if serial == "" {
		serial = DetectSerial()
	}
	if serial == "" {
		return "", nil, fmt.Errorf("could not auto-detect a serial number; pass --deployment-name")
	}
	macs := a.MACs
	if len(macs) == 0 {
		macs = DetectMACs()
	}
	if macsRequired && len(macs) == 0 {
		return "", nil, fmt.Errorf("could not auto-detect any MAC address; pass --macs")
	}
	return serial, macs, nil
}

// resolveSerial returns the device serial number: the DeploymentName flag first,
// then the auto-detected machine id (confirmed interactively in ManualMode),
// then an interactive prompt.
func (a *Agent) resolveSerial() (string, error) {
	if a.DeploymentName != "" {
		_, _ = fmt.Fprintf(a.promptOut(), "\x1b[32m✓\x1b[0m Deployment name: %s\n", a.DeploymentName)
		return a.DeploymentName, nil
	}
	detected := DetectSerial()
	if detected != "" && !a.ManualMode {
		_, _ = fmt.Fprintf(a.promptOut(), "\x1b[32m✓\x1b[0m Deployment name: %s\n", detected)
		return detected, nil
	}
	if detected != "" {
		choice, err := a.SelectFunc(
			fmt.Sprintf("Deployment name (detected: %s)", detected),
			[]string{detected, "enter manually"},
		)
		if err != nil {
			return "", err
		}
		if choice != "enter manually" {
			return choice, nil
		}
	}
	for {
		serial, err := a.InputFunc("Deployment name")
		if err != nil {
			return "", err
		}
		serial = strings.TrimSpace(serial)
		if serial != "" {
			return serial, nil
		}
		_, _ = fmt.Fprintln(a.Out, "Error: serial number is required")
	}
}

// resolveMACs returns the MAC address list: the MACs flag first, then the
// auto-detected addresses (confirmed interactively in ManualMode), then an
// interactive prompt.  When required is false (direct enrollment, where MACs
// are optional inventory metadata) an empty detection result is accepted
// without prompting.
func (a *Agent) resolveMACs(required bool) ([]string, error) {
	if len(a.MACs) > 0 {
		_, _ = fmt.Fprintf(a.promptOut(), "\x1b[32m✓\x1b[0m MAC addresses: %s\n", strings.Join(a.MACs, ", "))
		return a.MACs, nil
	}
	detected := DetectMACs()
	if !a.ManualMode {
		if len(detected) > 0 {
			_, _ = fmt.Fprintf(a.promptOut(), "\x1b[32m✓\x1b[0m MAC addresses: %s\n", strings.Join(detected, ", "))
			return detected, nil
		}
		if !required {
			return nil, nil
		}
	}
	if len(detected) > 0 {
		choice, err := a.SelectFunc(
			"MAC addresses:",
			[]string{
				fmt.Sprintf("use all detected (%d addresses)", len(detected)),
				"enter manually",
			},
		)
		if err != nil {
			return nil, err
		}
		if choice != "enter manually" {
			return detected, nil
		}
	}
	for {
		macs, err := a.promptMultipleMACs()
		if err != nil {
			return nil, err
		}
		if len(macs) > 0 || !required {
			return macs, nil
		}
		_, _ = fmt.Fprintln(a.Out, "Error: at least one MAC address is required")
	}
}

// printEnrollSummary prints the pre-enrollment confirmation block.
func (a *Agent) printEnrollSummary(req EnrollRequest) {
	_, _ = fmt.Fprintln(a.Out)
	_, _ = fmt.Fprintln(a.Out, "Enrolling:")
	_, _ = fmt.Fprintf(a.Out, "  Service:         %s\n", req.ServiceID)
	if req.DomainTemplateID != "" {
		_, _ = fmt.Fprintf(a.Out, "  Domain:          %s\n", req.DomainTemplateID)
	}
	_, _ = fmt.Fprintf(a.Out, "  Participant:     %s\n", req.ParticipantID)
	_, _ = fmt.Fprintf(a.Out, "  Deployment name: %s\n", req.Serial)
	if len(req.MACs) > 0 {
		_, _ = fmt.Fprintf(a.Out, "  MACs:            %s\n", strings.Join(req.MACs, ", "))
	}
	_, _ = fmt.Fprintln(a.Out)
}

// retryOrExit shows err and asks whether to retry.  Returns retry=true to
// continue the wizard loop; otherwise the error to abort with.
func (a *Agent) retryOrExit(err error) (bool, error) {
	_, _ = fmt.Fprintf(a.Out, "Error: %v\n\n", err)
	choice, err2 := a.SelectFunc("What would you like to do?", []string{"try again", "exit"})
	if err2 != nil {
		return false, err2
	}
	if choice == "exit" {
		return false, fmt.Errorf("enrollment cancelled")
	}
	return true, nil
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
		tui.Dim("Enroll with your Connext Cloud account (opens the login flow and"),
		tui.Dim("lets you pick the service and templates from lists), or paste a"),
		tui.Dim("campaign token issued by an operator (input will be hidden)."),
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
