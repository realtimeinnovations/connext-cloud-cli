// Package edgesyncagent implements the long-lived artifact lifecycle manager
// for rticloud edge-sync agent.
package edgesyncagent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
)

const renewalThreshold = 0.80 // renew at 80% of artifact lifetime

// ProfileState represents the lifecycle state of a Participant Profile.
type ProfileState string

const (
	StateUnregistered ProfileState = "unregistered"
	StateEnrolling    ProfileState = "enrolling"
	StateEnrolled     ProfileState = "enrolled"
	StateActive       ProfileState = "active"
	StateRenewing     ProfileState = "renewing"
)

// ArtifactID identifies one of the security artifacts managed by the agent.
type ArtifactID string

const (
	ArtifactIdentity    ArtifactID = "identity"
	ArtifactPermissions ArtifactID = "permissions"
	ArtifactPSK         ArtifactID = "psk"
	ArtifactCRL         ArtifactID = "crl"
	ArtifactDeviceCert  ArtifactID = "device-cert"

	// PSK rolling-key phase events.  These are single-shot timers that fire at
	// exact wall-clock times (not 80%-threshold); they do not appear in
	// allArtifacts and are handled separately by schedulePSKPhasesLocked.
	ArtifactPSKRotate  ArtifactID = "psk_rotate"  // 100%: rotate seed = sB
	ArtifactPSKCleanup ArtifactID = "psk_cleanup" // 120%: clear seed_extra
)

// allArtifacts is the ordered set of renewable artifacts whose 80%-threshold
// timers the agent manages via scheduleArtifactLocked.
var allArtifacts = []ArtifactID{ArtifactIdentity, ArtifactPermissions, ArtifactPSK, ArtifactCRL, ArtifactDeviceCert}

// displayArtifacts is the full list shown in the TUI.
var displayArtifacts = []ArtifactID{ArtifactIdentity, ArtifactPermissions, ArtifactPSK, ArtifactCRL, ArtifactDeviceCert}

// EnrollRequest is the JSON payload placed in the inbox directory to request
// enrollment of a new Participant Profile.
type EnrollRequest struct {
	ServiceID     string   `json:"service_id"`
	ParticipantID string   `json:"participant_id"`
	CampaignToken string   `json:"campaign_token"`
	Serial        string   `json:"serial"`
	MACs          []string `json:"macs"`
	DeviceName    string   `json:"device_name"`
}

// AgentState is the on-disk representation of a profile's artifact state.
// Written to <slot>/agent_state.json after each successful fetch or renewal.
type AgentState struct {
	State      ProfileState             `json:"state"`
	NotAfter   map[ArtifactID]time.Time `json:"not_after"`
	IssuedAt   map[ArtifactID]time.Time `json:"issued_at,omitempty"`
	DeviceName string                   `json:"device_name,omitempty"`

	// ServiceID is the edge provisioning service namespace (e.g. ces-alpha-123).
	// Used for CSR generation and API endpoint URL construction.
	// Empty in state files written before the domain_template_id folder
	// restructure; in that case the directory name doubles as serviceID.
	ServiceID string `json:"service_id,omitempty"`

	// DomainTemplateID is the second-level folder name under .connext/<serial>/:
	//   .connext/<serial>/<domain_template_id>/<participant_template_id>/
	// Empty in state files written before this field was introduced; falls
	// back to ServiceID (= directory name) when absent.
	DomainTemplateID string `json:"domain_template_id,omitempty"`

	// Serial is the device serial number that forms the root folder under .connext/:
	//   .connext/<serial>/<domain_template_id>/<participant_template_id>/
	// Empty in state files written before the serial-root restructure;
	// in that case the legacy 2-level layout is used.
	Serial string `json:"serial,omitempty"`

	// ParticipantTemplateID is the participant template identifier stored for
	// display and auditing purposes.  It matches the directory name at the
	// participant level and the participant_id field of the EnrollRequest.
	ParticipantTemplateID string `json:"participant_template_id,omitempty"`

	// PSK rolling-key protocol state.
	// PSKBNotAfter is the expiry of the "B" slot PSK obtained at enrollment
	// (or the most recently fetched sC at a prior 80% phase).  It is used to
	// schedule the PSKRotate and PSKCleanup timers for the next cycle.
	PSKBNotAfter time.Time `json:"psk_b_not_after,omitempty"`
	// PSKBaseTTL is the duration of sA's original lease (psk_a.notAfter −
	// issuedAt) and is used to compute the PSKCleanup fire-time (120% mark).
	PSKBaseTTL time.Duration `json:"psk_base_ttl,omitempty"`
}

// profile is the in-memory state for one serial+domain_template+participant triple.
type profile struct {
	mu               sync.Mutex
	serviceID        string // API / CSR org (e.g. ces-alpha-123)
	domainTemplateID string // second-level on-disk folder under <serial>/
	serial           string // root on-disk folder under .connext/
	participantID    string // participant template ID (directory name + request field)
	deviceName       string
	state            ProfileState
	notAfter         map[ArtifactID]time.Time
	issuedAt         map[ArtifactID]time.Time
	timers           map[ArtifactID]*time.Timer

	// PSK rolling-key protocol state (see AgentState for field semantics).
	pskBNotAfter time.Time
	pskBaseTTL   time.Duration
}

func (p *profile) setState(s ProfileState) { p.state = s }

// effectiveDomainID returns the domainTemplateID to use for store paths.
// When domainTemplateID has not been set (e.g. profiles created directly in
// tests or rehydrated from pre-migration state files), it falls back to
// serviceID so that existing on-disk layouts continue to work.
func (p *profile) effectiveDomainID() string {
	if p.domainTemplateID != "" {
		return p.domainTemplateID
	}
	return p.serviceID
}

// deviceSlot returns the composite participant ID used for store paths when
// a device name is present, creating a per-device subdirectory:
// .connext/<domain_template_id>/<participantID>/<deviceName>/
func deviceSlot(participantID, deviceName string) string {
	if deviceName == "" {
		return participantID
	}
	return filepath.Join(participantID, deviceName)
}

func (p *profile) storeParticipant() string {
	return deviceSlot(p.participantID, p.deviceName)
}

func profileKey(serviceID, participantID, deviceName string) string {
	// serviceID here is p.effectiveDomainID() — domainTemplateID when set,
	// falling back to serviceID for pre-migration profiles.  Including it in
	// the key ensures that two enrollments with the same participantID but
	// different domainTemplateIDs (different domain+participant combinations)
	// produce distinct in-memory entries and are not incorrectly merged.
	if deviceName == "" {
		return serviceID + "/" + participantID
	}
	return serviceID + "/" + participantID + "/" + deviceName
}

// Agent is the long-lived process managing the security artifact lifecycle for
// one or more Participant Profiles against an RTI Provisioning Service.
//
// Run blocks until ctx is cancelled.  All injectable fields follow the same
// conventions as GatewayApp: set in NewAgent with OS defaults, overridable for
// testing or by the runtime after construction.
type Agent struct {
	Out io.Writer
	// LogOut receives output that should go only to the log file (not the TUI).
	// It is set to the log-file writer when Run() opens LogFile; until then it
	// is io.Discard.  Renewal functions use this writer so that "saved to..."
	// messages are recorded in the log without polluting the live display.
	LogOut io.Writer
	Store  *edgestore.Store

	// Provisioning Service operations — wired by NewRuntime, overridable in tests.
	// EnrollFunc calls the enrollment API and returns the domain_template_id
	// from the server response (empty string if not present in the response).
	EnrollFunc             func(serviceID, participantID, serial string, macs []string, csrFile, keyFile, campaignToken, output string) (string, error)
	RequestIdentityFunc    func(url, cert, key, ca, serverAddr, participantID, csrFile, output string) error
	RequestPermissionsFunc func(url, cert, key, ca, serverAddr, participantID, output string) error
	RequestPSKFunc         func(url, cert, key, ca, serverAddr, output string) error
	GetCRLFunc             func(url, cert, key, ca, serverAddr, participantID, output string) error
	RenewDeviceCertFunc    func(url, cert, key, ca, serverAddr, csrFile string, validityMinutes int, output string) error
	GenerateKeyAndCSRFunc  func(commonName, org, tmpDir string) (keyPath, csrPath string, err error)
	GenerateCSRFromKeyFunc func(commonName, org string, keyPEM []byte, tmpDir string) (csrPath string, err error)
	DeriveDeviceURLFunc    func(serviceID string) string

	// Injectable clock and timer for deterministic testing.
	Now       func() time.Time
	AfterFunc func(d time.Duration, f func()) *time.Timer

	// Injectable file-system operations.
	ReadFile   func(string) ([]byte, error)
	WriteFile  func(string, []byte, os.FileMode) error
	MkdirAll   func(string, os.FileMode) error
	ReadDir    func(string) ([]fs.DirEntry, error)
	Rename     func(string, string) error
	RemoveFile func(string) error

	// Configuration.
	LogFile       string // Path to agent log file; created/appended by Run.
	InboxDir      string
	ProcessedDir  string
	FailedDir     string
	PollInterval  time.Duration
	SweepInterval time.Duration
	CRLInterval   time.Duration
	RetryInterval time.Duration // how long to wait before retrying a failed artifact renewal

	// Interactive I/O — used by the enrollment wizard and the live TUI.
	In         io.Reader
	SelectFunc func(message string, choices []string) (string, error)
	InputFunc  func(message string) (string, error)

	// Debug enables verbose HTTP request/response logging to LogOut.
	Debug bool

	// ManualMode, when true, prompts the user to confirm or override the
	// auto-detected serial number and MAC addresses during first-run enrollment.
	// When false (the default) the detected values are used directly without
	// any interactive selection step.
	ManualMode bool

	// Internal state.
	termOut  io.Writer          // original terminal writer for TUI rendering
	stopFunc context.CancelFunc // cancels the agent's context (Ctrl+C from TUI)
	profiles sync.Map           // profileKey → *profile
	wg       sync.WaitGroup
}

// NewAgent creates an Agent with production defaults for file I/O and timing.
// Provisioning Service function fields (EnrollFunc, RequestIdentityFunc, etc.)
// are left nil and must be wired by the caller (typically NewRuntime).
func NewAgent(store *edgestore.Store, out io.Writer) *Agent {
	a := &Agent{
		Out:           out,
		LogOut:        io.Discard,
		In:            os.Stdin,
		Store:         store,
		Now:           time.Now,
		AfterFunc:     time.AfterFunc,
		ReadFile:      os.ReadFile,
		WriteFile:     os.WriteFile,
		MkdirAll:      os.MkdirAll,
		ReadDir:       os.ReadDir,
		Rename:        os.Rename,
		RemoveFile:    os.Remove,
		InboxDir:      filepath.Join(store.BaseDir, "inbox"),
		ProcessedDir:  filepath.Join(store.BaseDir, "processed"),
		FailedDir:     filepath.Join(store.BaseDir, "failed"),
		PollInterval:  10 * time.Second,
		SweepInterval: 5 * time.Minute,
		CRLInterval:   5 * time.Minute,
		RetryInterval: 2 * time.Minute,
	}
	a.SelectFunc = a.defaultSelect
	a.InputFunc = a.defaultInput
	return a
}

func (a *Agent) defaultSelect(message string, choices []string) (string, error) {
	out := a.promptOut()
	sel := prompt.Selector{In: a.In, Out: out, CancelMessage: "Cancelled"}
	return sel.Select(message, choices)
}

func (a *Agent) defaultInput(message string) (string, error) {
	out := a.promptOut()
	inp := prompt.Input{In: a.In, Out: out, CancelMessage: "Cancelled"}
	return inp.Prompt(message)
}

// promptOut returns the raw terminal writer for interactive prompts.
// Using a.termOut (the original *os.File) ensures terminal-detection type
// assertions inside the prompt package succeed even after a.Out has been
// wrapped with io.MultiWriter for log-file tee-ing.
func (a *Agent) promptOut() io.Writer {
	if a.termOut != nil {
		return a.termOut
	}
	return a.Out
}

// Run starts the agent and blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	// Wrap the context so the TUI can stop the agent via Ctrl+C without
	// relying on OS signal delivery (which is suppressed in raw-terminal mode).
	ctx, a.stopFunc = context.WithCancel(ctx)
	defer a.stopFunc()

	// Preserve the original writer for TUI rendering (it must be an *os.File
	// for terminal detection). Log output goes through a.Out which may be
	// wrapped with a MultiWriter below.
	a.termOut = a.Out

	// Open log file — tee all output to both terminal and file.
	if a.LogFile != "" {
		if err := a.MkdirAll(filepath.Dir(a.LogFile), 0o755); err != nil {
			return fmt.Errorf("creating log directory: %w", err)
		}
		lf, err := os.OpenFile(a.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("opening log file %s: %w", a.LogFile, err)
		}
		defer lf.Close()
		logWriter := timestampWriter{w: lf}
		a.LogOut = logWriter
		a.Out = io.MultiWriter(a.Out, logWriter)
	}

	for _, dir := range []string{a.InboxDir, a.ProcessedDir, a.FailedDir} {
		if err := a.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}

	a.rehydrate()
	a.drainInbox()

	// First-run wizard: if no profiles were found, enroll one interactively.
	profileCount := 0
	a.profiles.Range(func(_, _ any) bool { profileCount++; return true })
	if profileCount == 0 {
		if err := a.ConfigureFirstRun(ctx); err != nil {
			return err
		}
	}

	a.wg.Add(2)
	go a.sweepLoop(ctx)
	go a.inboxLoop(ctx)

	a.runDisplay(ctx) // blocks until ctx is cancelled
	a.wg.Wait()
	_, _ = fmt.Fprintln(a.Out, "agent stopped")
	return nil
}

// ─── Startup rehydration ─────────────────────────────────────────────────────

// rehydrate walks the store looking for agent_state.json files and rebuilds
// the in-memory profile map and renewal timers without contacting the service.
//
// New layout (serial non-empty in state file):
//
//	.connext/<serial>/<domain_template_id>/<participant_id>[/<device_name>]/agent_state.json
//
// Legacy layouts (serial empty in state file):
//
//	.connext/<domain_template_id>/<participant_id>/agent_state.json
//	.connext/<domain_template_id>/<participant_id>/<device_name>/agent_state.json
func (a *Agent) rehydrate() {
	firstLevel, err := a.ReadDir(a.Store.BaseDir)
	if err != nil {
		return
	}
	for _, L1 := range firstLevel {
		if !L1.IsDir() {
			continue
		}
		L1Path := filepath.Join(a.Store.BaseDir, L1.Name())
		secondLevel, err := a.ReadDir(L1Path)
		if err != nil {
			continue
		}
		for _, L2 := range secondLevel {
			if !L2.IsDir() {
				continue
			}
			L2Path := filepath.Join(L1Path, L2.Name())

			// Check for agent_state.json at L2 level.
			// Legacy no-device: L1=domainTemplate, L2=participant
			if _, err := a.ReadFile(filepath.Join(L2Path, "agent_state.json")); err == nil {
				a.loadProfile("", L1.Name(), L2.Name(), "")
			}

			thirdLevel, err := a.ReadDir(L2Path)
			if err != nil {
				continue
			}
			for _, L3 := range thirdLevel {
				if !L3.IsDir() {
					continue
				}
				L3Path := filepath.Join(L2Path, L3.Name())

				// Check for agent_state.json at L3 level.
				// Could be:
				//   - Legacy with-device:   L1=domain, L2=participant, L3=device
				//   - New no-device:        L1=serial, L2=domain,      L3=participant
				// Disambiguate by reading the serial field from the state file.
				if stateData, err := a.ReadFile(filepath.Join(L3Path, "agent_state.json")); err == nil {
					var st AgentState
					if jsonErr := json.Unmarshal(stateData, &st); jsonErr == nil && st.Serial != "" {
						// New layout: L1=serial, L2=domain, L3=participant, no device
						a.loadProfile(L1.Name(), L2.Name(), L3.Name(), "")
					} else {
						// Legacy layout: L1=domain, L2=participant, L3=device
						a.loadProfile("", L1.Name(), L2.Name(), L3.Name())
					}
					continue
				}

				// No state file at L3 — walk L4 for new layout with device:
				//   L1=serial, L2=domain, L3=participant, L4=device
				fourthLevel, err := a.ReadDir(L3Path)
				if err != nil {
					continue
				}
				for _, L4 := range fourthLevel {
					if !L4.IsDir() {
						continue
					}
					if _, err := a.ReadFile(filepath.Join(L3Path, L4.Name(), "agent_state.json")); err == nil {
						a.loadProfile(L1.Name(), L2.Name(), L3.Name(), L4.Name())
					}
				}
			}
		}
	}
}

// loadProfile loads persisted state for one profile and schedules its timers.
// serial is the root folder (empty for legacy profiles).
// dirName is the domain_template_id (or serviceID for pre-migration profiles).
// participantID and deviceName identify the slot within dirName.
func (a *Agent) loadProfile(serial, dirName, participantID, deviceName string) {
	storePart := deviceSlot(participantID, deviceName)
	statePath := filepath.Join(a.Store.SlotDir(serial, dirName, storePart), "agent_state.json")
	data, err := a.ReadFile(statePath)
	if err != nil {
		return
	}
	var st AgentState
	if err := json.Unmarshal(data, &st); err != nil {
		_, _ = fmt.Fprintf(a.Out, "Warning: corrupt agent state file for %s/%s/%s, skipping: %v\n", dirName, participantID, deviceName, err)
		return
	}

	// Restore serviceID: for new enrollments it is stored explicitly; for
	// pre-migration profiles the directory name equals the serviceID.
	serviceID := st.ServiceID
	if serviceID == "" {
		serviceID = dirName
	}
	// Restore domainTemplateID: for new enrollments it is stored explicitly;
	// for pre-migration profiles the directory name equals the serviceID so
	// effectiveDomainID() falls back to serviceID automatically (empty string).
	domainTemplateID := st.DomainTemplateID

	// Restore serial: for new enrollments it is stored explicitly; for
	// pre-migration profiles it is empty (legacy 2-level path).
	restoredSerial := st.Serial
	if restoredSerial == "" {
		restoredSerial = serial
	}

	p := &profile{
		serviceID:        serviceID,
		domainTemplateID: domainTemplateID,
		serial:           restoredSerial,
		participantID:    participantID,
		deviceName:       st.DeviceName,
		state:            st.State,
		notAfter:         st.NotAfter,
		issuedAt:         st.IssuedAt,
		timers:           make(map[ArtifactID]*time.Timer),
		pskBNotAfter:     st.PSKBNotAfter,
		pskBaseTTL:       st.PSKBaseTTL,
	}
	if p.deviceName == "" {
		p.deviceName = deviceName
	}
	if p.notAfter == nil {
		p.notAfter = make(map[ArtifactID]time.Time)
	}
	if p.issuedAt == nil {
		p.issuedAt = make(map[ArtifactID]time.Time)
	}
	a.profiles.Store(profileKey(p.effectiveDomainID(), participantID, p.deviceName), p)
	a.scheduleAll(p)
	_, _ = fmt.Fprintf(a.Out, "profile rehydrated serial=%s service=%s domain=%s participant=%s device=%s state=%s\n",
		restoredSerial, serviceID, p.effectiveDomainID(), participantID, p.deviceName, p.state)
}

// ─── Timer scheduling ────────────────────────────────────────────────────────

// RenewalDelay returns the duration to wait before renewing an artifact whose
// validity window runs from notBefore to notAfter.  Returns 0 if the renewal
// threshold has already been passed.
//
// Exported for testing.
func RenewalDelay(now, notBefore, notAfter time.Time) time.Duration {
	if notAfter.IsZero() || !now.Before(notAfter) {
		return 0
	}
	lifetime := notAfter.Sub(notBefore)
	if lifetime <= 0 {
		return 0
	}
	threshold := notBefore.Add(time.Duration(float64(lifetime) * renewalThreshold))
	d := threshold.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

// scheduleAll schedules renewal timers for all known artifacts of p.
// 80%-threshold timers are set for allArtifacts; PSK phase timers (exact-time
// single-shot events) are handled separately by schedulePSKPhasesLocked.
func (a *Agent) scheduleAll(p *profile) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := a.Now()
	for _, artifact := range allArtifacts {
		notAfter, ok := p.notAfter[artifact]
		if !ok || notAfter.IsZero() {
			continue
		}
		a.scheduleArtifactLocked(p, artifact, now, notAfter)
	}
	a.schedulePSKPhasesLocked(p, now)
}

// schedulePSKPhasesLocked arms the single-shot PSK phase timers (PSKRotate at
// 100% and PSKCleanup at 120%).  Unlike the 80%-threshold timers, these fire
// at exact wall-clock instants stored in p.notAfter.  p.mu must be held.
func (a *Agent) schedulePSKPhasesLocked(p *profile, now time.Time) {
	for _, phase := range []ArtifactID{ArtifactPSKRotate, ArtifactPSKCleanup} {
		fireAt, ok := p.notAfter[phase]
		if !ok || fireAt.IsZero() {
			continue
		}
		a.scheduleAtLocked(p, phase, now, fireAt)
	}
}

// scheduleAtLocked sets a single-shot timer that fires at fireAt (not a
// percentage of lifetime).  p.mu must be held.
func (a *Agent) scheduleAtLocked(p *profile, phase ArtifactID, now, fireAt time.Time) {
	if t, ok := p.timers[phase]; ok && t != nil {
		t.Stop()
	}
	dispatch := func() {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			switch phase {
			case ArtifactPSKRotate:
				a.pskRotate(p)
			case ArtifactPSKCleanup:
				a.pskCleanup(p)
			}
		}()
	}
	d := fireAt.Sub(now)
	if d <= 0 {
		dispatch()
		return
	}
	p.timers[phase] = a.AfterFunc(d, dispatch)
}

// scheduleArtifactLocked replaces the renewal timer for one artifact.
// p.mu must be held by the caller.
func (a *Agent) scheduleArtifactLocked(p *profile, artifact ArtifactID, now, notAfter time.Time) {
	if t, ok := p.timers[artifact]; ok && t != nil {
		t.Stop()
	}
	notBefore := p.issuedAt[artifact]
	if notBefore.IsZero() {
		notBefore = now
	}
	d := RenewalDelay(now, notBefore, notAfter)
	if d <= 0 {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			a.renewArtifact(p, artifact, "past_threshold")
		}()
		return
	}
	scheduledNotAfter := notAfter
	p.timers[artifact] = a.AfterFunc(d, func() {
		a.onTimerFire(p, artifact, scheduledNotAfter)
	})
}

// onTimerFire is called when a per-artifact renewal timer fires.
// It guards against premature wakeups (system sleep, VM migration, clock
// drift): if the wall clock says the renewal threshold has not yet been
// reached, the timer is rescheduled.
func (a *Agent) onTimerFire(p *profile, artifact ArtifactID, scheduledNotAfter time.Time) {
	now := a.Now()
	p.mu.Lock()
	currentNotAfter := p.notAfter[artifact]
	notBefore := p.issuedAt[artifact]
	p.mu.Unlock()

	// The artifact was renewed since this timer was scheduled — ignore.
	if !currentNotAfter.Equal(scheduledNotAfter) {
		return
	}

	// Guard against premature wakeups (system sleep, VM migration, clock drift).
	// Re-evaluate against the artifact's real validity window so the threshold
	// stays at 80% of total lifetime. Passing now as not_before here would
	// compute 80% of the *remaining* time and defer renewal on every wakeup,
	// pushing it asymptotically toward not_after.
	if notBefore.IsZero() {
		notBefore = now
	}
	remaining := RenewalDelay(now, notBefore, scheduledNotAfter)
	if remaining > time.Second {
		// Fired prematurely — reschedule.
		p.mu.Lock()
		p.timers[artifact] = a.AfterFunc(remaining, func() {
			a.onTimerFire(p, artifact, scheduledNotAfter)
		})
		p.mu.Unlock()
		return
	}

	a.renewArtifact(p, artifact, "timer")
}

// ─── Artifact renewal ────────────────────────────────────────────────────────

// RenewArtifact requests an out-of-cycle renewal of one artifact for the
// identified profile. It stops the existing scheduled timer and immediately
// dispatches a renewal goroutine with reason "manual".
// effectiveDomainID is p.effectiveDomainID() for the target profile (i.e.
// domainTemplateID when set, otherwise serviceID).
func (a *Agent) RenewArtifact(effectiveDomainID, participantID, deviceName string, art ArtifactID) error {
	key := profileKey(effectiveDomainID, participantID, deviceName)
	val, ok := a.profiles.Load(key)
	if !ok {
		return fmt.Errorf("profile not found: %s", key)
	}
	p := val.(*profile)
	p.mu.Lock()
	if t, ok := p.timers[art]; ok && t != nil {
		t.Stop()
	}
	p.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.renewArtifact(p, art, "manual")
	}()
	return nil
}

// renewArtifact performs the renewal for one artifact and reschedules its timer.
func (a *Agent) renewArtifact(p *profile, artifact ArtifactID, reason string) {
	_, _ = fmt.Fprintf(a.LogOut, "artifact renewal started service=%s participant=%s artifact=%s reason=%s\n",
		p.serviceID, p.participantID, artifact, reason)

	p.mu.Lock()
	oldNotAfter := p.notAfter[artifact]
	p.setState(StateRenewing)
	p.mu.Unlock()

	url := a.Store.ResolveDeviceURL(p.serial, p.effectiveDomainID(), p.storeParticipant())
	cert, key, ca := a.Store.ResolveMTLSDefaults(p.serial, p.effectiveDomainID(), p.storeParticipant(), "", "", "")
	output := a.Store.ConnextArtifactsDir(p.serial, p.effectiveDomainID(), p.storeParticipant()) + string(os.PathSeparator)

	var newNotAfter time.Time
	var newNotBefore time.Time
	var err error

	switch artifact {
	case ArtifactIdentity:
		newNotAfter, err = a.renewIdentity(p, url, cert, key, ca, output)
		if err == nil {
			leasePath := filepath.Join(strings.TrimSuffix(output, string(os.PathSeparator)), "identity_lease.json")
			newNotBefore, _ = a.readLease(leasePath)
		}
	case ArtifactPermissions:
		if err = a.RequestPermissionsFunc(url, cert, key, ca, "", p.participantID, output); err == nil {
			leasePath := filepath.Join(strings.TrimSuffix(output, string(os.PathSeparator)), "permissions_lease.json")
			newNotBefore, newNotAfter = a.readLease(leasePath)
		}
	case ArtifactPSK:
		var pskBNA time.Time
		newNotAfter, pskBNA, err = a.renewPSKAt80(p, url, cert, key, ca, output)
		if err == nil {
			// Reschedule PSK phase timers (PSKRotate / PSKCleanup) for the next cycle.
			now2 := a.Now()
			p.mu.Lock()
			p.notAfter[ArtifactPSKRotate] = p.pskBNotAfter
			if p.pskBaseTTL > 0 {
				p.notAfter[ArtifactPSKCleanup] = p.pskBNotAfter.Add(p.pskBaseTTL / 5) // +20 %
			}
			// Advance pskBNotAfter to the new sB's notAfter for the next cycle.
			// Use sB's value if available; fall back to sA's.
			if !pskBNA.IsZero() {
				p.pskBNotAfter = pskBNA
			} else {
				p.pskBNotAfter = newNotAfter
			}
			a.schedulePSKPhasesLocked(p, now2)
			p.mu.Unlock()
		}
	case ArtifactCRL:
		err = a.GetCRLFunc(url, cert, key, ca, "", p.participantID, output)
		if err == nil {
			newNotAfter = a.Now().Add(a.CRLInterval)
		}
	case ArtifactDeviceCert:
		newNotAfter, err = a.renewDeviceCert(p, url, cert, key, ca)
	}

	if err != nil {
		_, _ = fmt.Fprintf(a.LogOut, "artifact renewal failed service=%s participant=%s artifact=%s err=%v\n",
			p.serviceID, p.participantID, artifact, err)
		p.mu.Lock()
		p.setState(StateActive)
		notAfter := p.notAfter[artifact]
		p.timers[artifact] = a.AfterFunc(a.RetryInterval, func() {
			a.onTimerFire(p, artifact, notAfter)
		})
		p.mu.Unlock()
		return
	}

	p.mu.Lock()
	if !newNotAfter.IsZero() {
		p.notAfter[artifact] = newNotAfter
		if !newNotBefore.IsZero() {
			p.issuedAt[artifact] = newNotBefore
		} else {
			p.issuedAt[artifact] = a.Now()
		}
	}
	p.setState(StateActive)
	p.mu.Unlock()

	_, _ = fmt.Fprintf(a.LogOut, "artifact renewal complete service=%s participant=%s artifact=%s old_not_after=%s new_not_after=%s\n",
		p.serviceID, p.participantID, artifact, oldNotAfter.Format(time.RFC3339), newNotAfter.Format(time.RFC3339))

	if err := a.persistState(p); err != nil {
		_, _ = fmt.Fprintf(a.Out, "Warning: could not persist agent state: %v\n", err)
	}

	// Guard against immediate retry: if newNotAfter didn't advance (stale
	// lease or server returned same expiry), back off instead of looping.
	now := a.Now()
	if newNotAfter.IsZero() || !newNotAfter.After(oldNotAfter) || !now.Before(newNotAfter) {
		p.mu.Lock()
		p.timers[artifact] = a.AfterFunc(a.RetryInterval, func() {
			a.onTimerFire(p, artifact, newNotAfter)
		})
		p.mu.Unlock()
		return
	}

	p.mu.Lock()
	a.scheduleArtifactLocked(p, artifact, now, newNotAfter)
	p.mu.Unlock()
}

// identityKeyPath returns the on-disk path of the DDS identity certificate's
// dedicated private key. This key is intentionally distinct from the mTLS
// device key (Store.PrivateKeyPath) so the DDS Security identity credential and
// the provisioning-transport credential live in separate trust domains.
func (a *Agent) identityKeyPath(p *profile) string {
	return filepath.Join(
		a.Store.ConnextArtifactsDir(p.serial, p.effectiveDomainID(), p.storeParticipant()),
		"identity_key.pem",
	)
}

// renewIdentity creates a CSR for the DDS identity certificate using a dedicated
// identity key (NOT the mTLS device key) and calls RequestIdentityFunc. The
// dedicated key is generated on first use (enrollment, or first renewal after
// upgrade) and reused across renewals thereafter.
func (a *Agent) renewIdentity(p *profile, url, cert, key, ca, output string) (time.Time, error) {
	tmpDir, err := os.MkdirTemp("", "rticloud-agent-csr-*")
	if err != nil {
		return time.Time{}, err
	}
	defer os.RemoveAll(tmpDir)

	idKeyPath := a.identityKeyPath(p)

	var csrPath string
	var freshKeyTmp string // non-empty only when we generated a new identity key this cycle

	existingIDKey, readErr := a.ReadFile(idKeyPath)
	if readErr != nil {
		// No dedicated identity key yet: generate one. Keep CN/org identical to
		// the previous behavior ("device" / serviceID) so server-side identity
		// issuance is unaffected.
		freshKeyTmp, csrPath, err = a.GenerateKeyAndCSRFunc("device", p.serviceID, tmpDir)
		if err != nil {
			return time.Time{}, fmt.Errorf("generating identity key and CSR: %w", err)
		}
	} else {
		// Reuse the existing dedicated identity key.
		csrPath, err = a.GenerateCSRFromKeyFunc("device", p.serviceID, existingIDKey, tmpDir)
		if err != nil {
			return time.Time{}, fmt.Errorf("generating CSR from identity key: %w", err)
		}
	}

	if err := a.RequestIdentityFunc(url, cert, key, ca, "", p.participantID, csrPath, output); err != nil {
		return time.Time{}, err
	}

	// Commit a freshly generated key only after the certificate was issued, so
	// the on-disk key matches the on-disk identity certificate.
	if freshKeyTmp != "" {
		newKeyPEM, err := a.ReadFile(freshKeyTmp)
		if err != nil {
			return time.Time{}, fmt.Errorf("reading generated identity key: %w", err)
		}
		if err := a.MkdirAll(filepath.Dir(idKeyPath), 0o755); err != nil {
			return time.Time{}, fmt.Errorf("creating identity key dir: %w", err)
		}
		if err := a.WriteFile(idKeyPath, newKeyPEM, 0o600); err != nil {
			return time.Time{}, fmt.Errorf("writing identity key: %w", err)
		}
	}

	leasePath := filepath.Join(strings.TrimSuffix(output, string(os.PathSeparator)), "identity_lease.json")
	return a.readLeaseNotAfter(leasePath), nil
}

// renewDeviceCert renews the mTLS device certificate using the same key pair
// by calling POST /device/renew-cert.  The renewed certificate is saved to
// mtls_artifacts/device.crt and the new NotAfter is read from the certificate.
func (a *Agent) renewDeviceCert(p *profile, url, cert, key, ca string) (time.Time, error) {
	existingKey, err := a.ReadFile(a.Store.PrivateKeyPath(p.serial, p.effectiveDomainID(), p.storeParticipant()))
	if err != nil {
		return time.Time{}, fmt.Errorf("reading existing device key: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "rticloud-agent-renew-cert-*")
	if err != nil {
		return time.Time{}, err
	}
	defer os.RemoveAll(tmpDir)

	csrPath, err := a.GenerateCSRFromKeyFunc("device", p.serviceID, existingKey, tmpDir)
	if err != nil {
		return time.Time{}, fmt.Errorf("generating CSR from existing key: %w", err)
	}

	mtlsOutput := a.Store.MTLSDir(p.serial, p.effectiveDomainID(), p.storeParticipant()) + string(os.PathSeparator)
	if err := a.RenewDeviceCertFunc(url, cert, key, ca, "", csrPath, 0, mtlsOutput); err != nil {
		return time.Time{}, err
	}

	return a.readCertNotAfter(a.Store.DeviceCertPath(p.serial, p.effectiveDomainID(), p.storeParticipant())), nil
}

// ─── PSK rolling-key protocol ────────────────────────────────────────────────
//
// Since SPARK-5, every POST /psk call always returns both PSKs:
//
//	psk_primary.txt  — active seed (seed), used by DDS immediately.
//	psk_extra.txt    — seed_extra; populated during the 80–100 % overlap window
//	                   so peers that have the old seed can still communicate.
//	psk_temp.txt     — staging: holds the next primary (sB) so pskRotate can
//	                   apply it at 100% without calling the server again.  Never
//	                   read by DDS.
//
// Timeline (T = sA's TTL):
//
//	 0        0.8T      1.0T     1.2T
//	 |─────────|─────────|────────|────────>
//	           80%      100%    120%
//	           ↓         ↓        ↓
//	    set extra=sA+sB  primary=sB  clear extra
//	    call /psk (→sA,sB)
//	    temp=sB
//
// initializePSKFiles is called once after the first RequestPSKFunc at
// enrollment; renewPSKAt80 handles the 80% phase; pskRotate handles 100%;
// pskCleanup handles 120%.

// pskPrimaryPath / pskExtraPath / pskTempPath / pskLeasePath return the
// canonical file paths for the four PSK files under a profile's output dir.
func (a *Agent) pskOutDir(p *profile) string {
	return a.Store.ConnextArtifactsDir(p.serial, p.effectiveDomainID(), p.storeParticipant())
}

func pskPrimaryPath(outDir string) string { return filepath.Join(outDir, "psk_primary.txt") }
func pskExtraPath(outDir string) string   { return filepath.Join(outDir, "psk_extra.txt") }
func pskTempPath(outDir string) string    { return filepath.Join(outDir, "psk_temp.txt") }
func pskLeasePath(outDir string) string   { return filepath.Join(outDir, "psk_lease.json") }

// initializePSKFiles sets up the canonical PSK file layout after the first
// RequestPSKFunc call at enrollment.
//
// Since SPARK-5, RequestPSKFunc always writes:
//
//	psk_primary.txt = sA  (lower passphrase_id slot)
//	psk_extra.txt   = sA + "\n" + sB
//	psk_lease.json  = leases for both slots
//
// This function re-arranges those files to match the rolling-key initial state:
//
//	psk_primary.txt = sA  (unchanged — active seed)
//	psk_extra.txt   = ""  (empty — no overlap needed yet)
//	psk_temp.txt    = sB  (next primary, staged for pskRotate at 100%)
//
// It also populates the PSK phase timer entries in p and sets p.pskBNotAfter
// and p.pskBaseTTL.  p.mu must NOT be held by the caller.
func (a *Agent) initializePSKFiles(p *profile, outDir string, enrolledAt time.Time) {
	extraPath := pskExtraPath(outDir)
	tempPath := pskTempPath(outDir)
	leasePath := pskLeasePath(outDir)

	// Normalize psk_primary.txt to a single clean line. The server may write it
	// with a trailing newline, which DDS rejects with
	// "invalid format for the pre-shared secret passphrases".
	primaryPath := pskPrimaryPath(outDir)
	if data, err := a.ReadFile(primaryPath); err == nil {
		_ = a.WriteFile(primaryPath, []byte(pskFirstLine(data)), 0o644)
	}

	// Extract sB (second line of psk_extra.txt) and store it as psk_temp.txt
	// so pskRotate can apply it at the 100% phase without contacting the server.
	if data, err := a.ReadFile(extraPath); err == nil {
		sB := pskSecondLine(data)
		_ = a.WriteFile(tempPath, []byte(sB), 0o644)
	}
	// Set extra to sB (the staged next primary) so extra is non-empty and
	// holds the upcoming key.  This satisfies the protocol invariant:
	// primary=sA, extra=sB (the next key, never the same as primary).
	if tempData, err := a.ReadFile(tempPath); err == nil {
		_ = a.WriteFile(extraPath, []byte(pskFirstLine(tempData)), 0o644)
	} else {
		_ = a.WriteFile(extraPath, []byte{}, 0o644)
	}

	// Read individual slot leases to set up phase timers.
	pskANotAfter, pskBNotAfter := a.readPSKABNotAfter(leasePath)

	p.mu.Lock()
	defer p.mu.Unlock()

	// Override the notAfter already set by enrollProfile with psk_a's expiry.
	// The 80% timer fires at 80% of sA's lifetime.
	if !pskANotAfter.IsZero() {
		p.notAfter[ArtifactPSK] = pskANotAfter
		p.issuedAt[ArtifactPSK] = enrolledAt
	}

	// PSKRotate fires at sA's notAfter (= 100% mark).
	if !pskANotAfter.IsZero() {
		p.notAfter[ArtifactPSKRotate] = pskANotAfter
	}

	baseTTL := pskANotAfter.Sub(enrolledAt)
	if baseTTL > 0 {
		p.pskBaseTTL = baseTTL
		// PSKCleanup fires at 120% = sA.notAfter + 20% of baseTTL.
		p.notAfter[ArtifactPSKCleanup] = pskANotAfter.Add(baseTTL / 5)
	}

	// Store sB's expiry so the 80% handler can schedule the next rotate/cleanup.
	p.pskBNotAfter = pskBNotAfter
}

// renewPSKAt80 executes the 80% phase of the PSK rolling-key protocol.
//
// Since SPARK-5, RequestPSKFunc always returns both PSKs, so the server
// already writes the correct psk_primary.txt and psk_extra.txt.  This
// function's responsibility is:
//
//  1. Save the current primary (sA) before calling the server.
//  2. Call RequestPSKFunc → server writes psk_primary.txt = sA,
//     psk_extra.txt = sA+sB (the overlap pair).
//  3. Verify the server's psk_a matches our saved primary — if not, the
//     server has already rotated and we log a warning.
//  4. Extract sB (second line of psk_extra.txt) → write psk_temp.txt = sB
//     so that pskRotate can promote it at 100% without a server call.
//
// Returns the new notAfter for the ArtifactPSK 80%-threshold timer
// (= psk_a's notAfter) and psk_b's notAfter for advancing pskBNotAfter.
func (a *Agent) renewPSKAt80(p *profile, url, cert, key, ca, output string) (pskANotAfter, pskBNotAfter time.Time, err error) {
	outDir := strings.TrimSuffix(output, string(os.PathSeparator))
	primaryPath := pskPrimaryPath(outDir)
	extraPath := pskExtraPath(outDir)
	tempPath := pskTempPath(outDir)
	leasePath := pskLeasePath(outDir)

	// 1. Save the current seed before the server call so we can detect
	//    unexpected rotation and build the extra overlap if needed.
	savedPrimary, readErr := a.ReadFile(primaryPath)
	if readErr != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("psk 80%%: reading psk_primary.txt: %w", readErr)
	}

	// 2. Call the server. RequestPSKFunc writes:
	//      psk_primary.txt = psk_a (active seed)
	//      psk_extra.txt   = psk_a + "\n" + psk_b (overlap pair)
	//      psk_lease.json  = leases for both slots
	if callErr := a.RequestPSKFunc(url, cert, key, ca, "", output); callErr != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("psk 80%%: fetching next PSK: %w", callErr)
	}

	// 3. Check whether the server's psk_a matches our saved primary.
	//    In the normal path both are equal (sA is still active at 80%).
	//    A mismatch means the server has already rotated ahead of our timer.
	receivedPrimary, _ := a.ReadFile(primaryPath)
	// Use only the first line for comparison — savedPrimary may be corrupted
	// (multi-line) from a previous bug; the server always returns a single value.
	savedStr := pskFirstLine(savedPrimary)
	receivedStr := pskFirstLine(receivedPrimary)
	if receivedStr != savedStr {
		_, _ = fmt.Fprintf(a.LogOut,
			"psk 80%%: WARNING: server primary (%q) differs from local primary (%q) — "+
				"server has already rotated; keeping server value service=%s participant=%s\n",
			receivedStr, savedStr, p.serviceID, p.participantID)
		// Prepend the old primary so any peers still using it can decrypt.
		// Deduplicate and limit to 2 entries to prevent cascading corruption.
		extraData, _ := a.ReadFile(extraPath)
		augmented := pskDedupLines(append([]string{savedStr}, pskSplitLines(extraData)...))
		if len(augmented) > 2 {
			augmented = augmented[len(augmented)-2:]
		}
		_ = a.WriteFile(extraPath, []byte(strings.Join(augmented, "\n")), 0o644)
	}
	// Normalize psk_primary.txt to a single clean line regardless of rotation
	// path — DDS rejects any trailing newline or multi-line content.
	currentPrimary := pskFirstLine(receivedPrimary)
	_ = a.WriteFile(primaryPath, []byte(currentPrimary), 0o644)

	// 4. Extract sB (second line of psk_extra.txt) and stage it in psk_temp.txt
	//    so pskRotate can apply it at 100% without a server call.
	extraData, readExtraErr := a.ReadFile(extraPath)
	if readExtraErr != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("psk 80%%: reading psk_extra.txt: %w", readExtraErr)
	}
	// Stage sB as a single line — pskRotate must never receive multi-line content.
	nextPrimary := pskFirstLine([]byte(pskSecondLine(extraData)))
	if err := a.WriteFile(tempPath, []byte(nextPrimary), 0o644); err != nil {
		_, _ = fmt.Fprintf(a.LogOut, "psk 80%%: writing psk_temp.txt: %v\n", err)
	}

	// Strip the current primary from extra so primary and extra never share the
	// same key, and extra never exceeds 2 entries.
	// Result: extra = [sB] only (the next key, ready for the overlap window).
	extraLines := pskSplitLines(extraData)
	var filteredLines []string
	for _, l := range extraLines {
		if l != currentPrimary {
			filteredLines = append(filteredLines, l)
		}
	}
	if len(filteredLines) > 2 {
		filteredLines = filteredLines[len(filteredLines)-2:]
	}
	_ = a.WriteFile(extraPath, []byte(strings.Join(filteredLines, "\n")), 0o644)

	// Return psk_a's notAfter for the 80%-threshold timer and psk_b's notAfter
	// so the caller can correctly advance pskBNotAfter for the next cycle.
	pskA, pskB := a.readPSKABNotAfter(leasePath)
	return pskA, pskB, nil
}

// pskRotate executes the 100% phase: rotates psk_primary.txt to sB.
// psk_temp.txt contains sB (the single next primary staged by renewPSKAt80).
// After rotation, extra is set to the old primary (sA) to keep the overlap
// window open for peers that haven't yet received the new primary.
func (a *Agent) pskRotate(p *profile) {
	outDir := a.pskOutDir(p)
	tempData, err := a.ReadFile(pskTempPath(outDir))
	if err != nil {
		_, _ = fmt.Fprintf(a.LogOut, "psk_rotate: reading psk_temp.txt service=%s participant=%s: %v\n",
			p.serviceID, p.participantID, err)
		return
	}
	// Take only the first line — guard against multi-line psk_temp.txt corruption.
	nextPrimary := pskFirstLine(tempData)

	// Read current primary (sA) before overwriting so we can set it as extra
	// for the 100%–120% overlap window.
	oldPrimaryData, _ := a.ReadFile(pskPrimaryPath(outDir))
	oldPrimary := pskFirstLine(oldPrimaryData)

	if err := a.WriteFile(pskPrimaryPath(outDir), []byte(nextPrimary), 0o644); err != nil {
		_, _ = fmt.Fprintf(a.LogOut, "psk_rotate: writing psk_primary.txt service=%s participant=%s: %v\n",
			p.serviceID, p.participantID, err)
		return
	}

	// Set extra to sA so peers still using the old primary can decrypt during
	// the 100%–120% overlap window.  extra ≠ primary by construction.
	if oldPrimary != "" {
		_ = a.WriteFile(pskExtraPath(outDir), []byte(oldPrimary), 0o644)
	}

	_, _ = fmt.Fprintf(a.LogOut, "psk_rotate: seed rotated to sB service=%s participant=%s\n",
		p.serviceID, p.participantID)

	// Update ArtifactPSKRotate to sB's expiry (= pskBNotAfter) so the TUI
	// always shows the current key's expiration time after rotation.
	p.mu.Lock()
	if !p.pskBNotAfter.IsZero() {
		p.notAfter[ArtifactPSKRotate] = p.pskBNotAfter
	} else {
		delete(p.notAfter, ArtifactPSKRotate)
	}
	p.mu.Unlock()

	if err := a.persistState(p); err != nil {
		_, _ = fmt.Fprintf(a.Out, "Warning: could not persist agent state after psk_rotate: %v\n", err)
	}
}

// pskCleanup executes the 120% phase: clears psk_extra.txt to close the
// overlap window.  At this point sA is expired and no peers should still be
// using it, so the overlap entry (set by pskRotate) is no longer needed.
func (a *Agent) pskCleanup(p *profile) {
	outDir := a.pskOutDir(p)
	if err := a.WriteFile(pskExtraPath(outDir), []byte{}, 0o644); err != nil {
		_, _ = fmt.Fprintf(a.LogOut, "psk_cleanup: clearing psk_extra.txt service=%s participant=%s: %v\n",
			p.serviceID, p.participantID, err)
		return
	}
	_, _ = fmt.Fprintf(a.LogOut, "psk_cleanup: seed_extra cleared service=%s participant=%s\n",
		p.serviceID, p.participantID)

	p.mu.Lock()
	delete(p.notAfter, ArtifactPSKCleanup)
	p.mu.Unlock()

	if err := a.persistState(p); err != nil {
		_, _ = fmt.Fprintf(a.Out, "Warning: could not persist agent state after psk_cleanup: %v\n", err)
	}
}

// pskFirstLine returns the first non-empty line of data, stripped of
// surrounding whitespace.  It never returns multi-line content.
func pskFirstLine(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// pskSplitLines splits data into non-empty trimmed lines.
func pskSplitLines(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// pskDedupLines returns a deduplicated slice preserving order and original values.
func pskDedupLines(lines []string) []string {
	seen := make(map[string]bool, len(lines))
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// pskSecondLine returns the second newline-delimited value in data, or the
// first (and only) value when there is no second line.  Trailing newlines are
// stripped before splitting so "sA\nsB\n" correctly yields "sB".  The return
// value is always a single line (never contains embedded newlines).
func pskSecondLine(data []byte) string {
	lines := pskSplitLines(data)
	if len(lines) > 1 {
		return lines[1]
	}
	if len(lines) == 1 {
		return lines[0]
	}
	return ""
}

// readPSKABNotAfter reads psk_a and psk_b not_after timestamps from a psk_lease.json.
func (a *Agent) readPSKABNotAfter(path string) (pskA, pskB time.Time) {
	data, err := a.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	readSlot := func(key string) time.Time {
		slotData, ok := raw[key]
		if !ok {
			return time.Time{}
		}
		var slot struct {
			Lease struct {
				NotAfter time.Time `json:"not_after"`
			} `json:"lease"`
		}
		if err := json.Unmarshal(slotData, &slot); err != nil {
			return time.Time{}
		}
		return slot.Lease.NotAfter
	}
	pskA = readSlot("psk_a")
	pskB = readSlot("psk_b")
	return
}

// ─────────────────────────────────────────────────────────────────────────────

// readLease reads a *_lease.json file and returns both not_before and not_after.
// Returns zero times if the file cannot be read or a field is absent.
func (a *Agent) readLease(path string) (notBefore, notAfter time.Time) {
	data, err := a.ReadFile(path)
	if err != nil {
		return
	}
	var wrapper struct {
		Lease struct {
			NotBefore time.Time `json:"not_before"`
			NotAfter  time.Time `json:"not_after"`
		} `json:"lease"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return
	}
	return wrapper.Lease.NotBefore, wrapper.Lease.NotAfter
}

// readLeaseNotAfter reads a *_lease.json file and returns the not_after timestamp.
// Returns the zero time if the file cannot be read or the field is absent.
func (a *Agent) readLeaseNotAfter(path string) time.Time {
	_, notAfter := a.readLease(path)
	return notAfter
}

// readPSKLeaseNotAfter reads a psk_lease.json file (which has per-slot leases)
// and returns the earliest not_after across all slots.
func (a *Agent) readPSKLeaseNotAfter(path string) time.Time {
	data, err := a.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return time.Time{}
	}
	var earliest time.Time
	for _, key := range []string{"psk_a", "psk_b", "psk"} {
		slotData, ok := raw[key]
		if !ok {
			continue
		}
		var slot struct {
			Lease struct {
				NotAfter time.Time `json:"not_after"`
			} `json:"lease"`
		}
		if err := json.Unmarshal(slotData, &slot); err != nil {
			continue
		}
		na := slot.Lease.NotAfter
		if !na.IsZero() && (earliest.IsZero() || na.Before(earliest)) {
			earliest = na
		}
	}
	return earliest
}

// readCertNotAfter parses a PEM-encoded certificate file and returns NotAfter.
func (a *Agent) readCertNotAfter(path string) time.Time {
	data, err := a.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return time.Time{}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}
	}
	return cert.NotAfter
}

// persistState writes the profile's current state to agent_state.json.
func (a *Agent) persistState(p *profile) error {
	p.mu.Lock()
	st := AgentState{
		State:                 p.state,
		DeviceName:            p.deviceName,
		ServiceID:             p.serviceID,
		DomainTemplateID:      p.domainTemplateID,
		Serial:                p.serial,
		ParticipantTemplateID: p.participantID,
		NotAfter:              make(map[ArtifactID]time.Time, len(p.notAfter)),
		IssuedAt:              make(map[ArtifactID]time.Time, len(p.issuedAt)),
		PSKBNotAfter:          p.pskBNotAfter,
		PSKBaseTTL:            p.pskBaseTTL,
	}
	for k, v := range p.notAfter {
		st.NotAfter[k] = v
	}
	for k, v := range p.issuedAt {
		st.IssuedAt[k] = v
	}
	p.mu.Unlock()

	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	slotDir := a.Store.SlotDir(p.serial, p.effectiveDomainID(), p.storeParticipant())
	if err := a.MkdirAll(slotDir, 0o755); err != nil {
		return err
	}
	return a.WriteFile(filepath.Join(slotDir, "agent_state.json"), append(data, '\n'), 0o644)
}

// ─── Background loops ────────────────────────────────────────────────────────

func (a *Agent) sweepLoop(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(a.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sweep()
		}
	}
}

// sweep reconciles all artifact timers against current on-disk notAfter values
// and triggers immediate renewal for any artifact past its threshold.
func (a *Agent) sweep() {
	now := a.Now()
	a.profiles.Range(func(_, val any) bool {
		p := val.(*profile)
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, artifact := range allArtifacts {
			notAfter, ok := p.notAfter[artifact]
			if !ok || notAfter.IsZero() {
				continue
			}
			notBefore := p.issuedAt[artifact]
			if notBefore.IsZero() {
				notBefore = now
			}
			if RenewalDelay(now, notBefore, notAfter) <= 0 {
				if t, ok := p.timers[artifact]; ok && t != nil {
					t.Stop()
				}
				a.wg.Add(1)
				capturedArtifact := artifact
				go func() {
					defer a.wg.Done()
					a.renewArtifact(p, capturedArtifact, "sweep")
				}()
			}
		}
		// Sweep PSK phase timers (exact-time single-shot events).
		a.schedulePSKPhasesLocked(p, now)
		_, _ = fmt.Fprintf(a.Out, "sweep status service=%s participant=%s state=%s\n",
			p.serviceID, p.participantID, p.state)
		return true
	})
}

func (a *Agent) inboxLoop(ctx context.Context) {
	defer a.wg.Done()
	ticker := time.NewTicker(a.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.drainInbox()
		}
	}
}

// ─── Inbox processing ────────────────────────────────────────────────────────

// drainInbox processes all pending enroll-*.json files in the inbox directory.
func (a *Agent) drainInbox() {
	entries, err := a.ReadDir(a.InboxDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "enroll-") ||
			!strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		a.processInboxFile(filepath.Join(a.InboxDir, name))
	}
}

// processInboxFile validates and processes a single enrollment request file.
func (a *Agent) processInboxFile(path string) {
	data, err := a.ReadFile(path)
	if err != nil {
		_, _ = fmt.Fprintf(a.Out, "inbox read failed path=%s err=%v\n", path, err)
		return
	}
	var req EnrollRequest
	if err := json.Unmarshal(data, &req); err != nil {
		_, _ = fmt.Fprintf(a.Out, "inbox invalid JSON path=%s err=%v\n", path, err)
		a.moveInboxFile(path, a.FailedDir, "parse error: "+err.Error())
		return
	}
	if req.ServiceID == "" || req.ParticipantID == "" || req.Serial == "" || len(req.MACs) == 0 {
		_, _ = fmt.Fprintf(a.Out, "inbox missing required fields path=%s\n", path)
		a.moveInboxFile(path, a.FailedDir, "missing required fields: service_id, participant_id, serial, macs")
		return
	}

	_, _ = fmt.Fprintf(a.Out, "inbox enrollment request received service=%s participant=%s\n",
		req.ServiceID, req.ParticipantID)

	if err := a.enrollProfile(req); err != nil {
		_, _ = fmt.Fprintf(a.Out, "inbox enrollment failed service=%s participant=%s err=%v\n",
			req.ServiceID, req.ParticipantID, err)
		a.moveInboxFile(path, a.FailedDir, err.Error())
		return
	}
	a.moveInboxFile(path, a.ProcessedDir, "")
	_, _ = fmt.Fprintf(a.Out, "inbox enrollment complete service=%s participant=%s\n",
		req.ServiceID, req.ParticipantID)
}

// enrollProfile runs the full enrollment + artifact-fetch sequence for a new profile.
func (a *Agent) enrollProfile(req EnrollRequest) error {
	p := a.getOrCreateProfile(req.ServiceID, req.ParticipantID, req.DeviceName)

	p.mu.Lock()
	p.serial = req.Serial // set serial upfront so store paths include it from the start
	p.setState(StateEnrolling)
	p.mu.Unlock()

	tmpDir, err := os.MkdirTemp("", "rticloud-agent-enroll-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	keyPath, csrPath, err := a.GenerateKeyAndCSRFunc("device", req.ServiceID, tmpDir)
	if err != nil {
		p.mu.Lock()
		p.setState(StateUnregistered)
		p.mu.Unlock()
		return fmt.Errorf("generating CSR: %w", err)
	}

	// Pass storePart as participantID so EnrollDevice writes directly to the
	// device-level slot (participantID/deviceName) without any post-enrollment
	// relocation.  The hint output uses serial+serviceID since domainTemplateID
	// is not yet known.
	storePart := deviceSlot(req.ParticipantID, req.DeviceName)
	hintOutput := a.Store.ConnextArtifactsDir(req.Serial, req.ServiceID, storePart) + string(os.PathSeparator)

	domainTemplateID, enrollErr := a.EnrollFunc(req.ServiceID, storePart, req.Serial, req.MACs, csrPath, keyPath, req.CampaignToken, hintOutput)
	if enrollErr != nil {
		// HTTP 409 means the device is already enrolled in this campaign (e.g. a
		// previous attempt succeeded but a later step failed).  If the device key
		// is already stored we can skip re-enrollment and proceed directly to
		// artifact fetching using the existing credentials.
		effectiveID := domainTemplateID
		if effectiveID == "" {
			effectiveID = req.ServiceID
		}
		keyAlreadyStored := func() bool {
			_, err := a.ReadFile(a.Store.PrivateKeyPath(req.Serial, effectiveID, storePart))
			return err == nil
		}
		alreadyEnrolled := strings.Contains(enrollErr.Error(), "409")
		if !alreadyEnrolled || !keyAlreadyStored() {
			p.mu.Lock()
			p.setState(StateUnregistered)
			p.mu.Unlock()
			return fmt.Errorf("enrollment: %w", enrollErr)
		}
		// Already enrolled: fall through using the stored credentials.
		_, _ = fmt.Fprintf(a.Out, "Note: device already enrolled; resuming artifact fetch.\n")
	}

	// Record the domainTemplateID on the profile and re-key it in the profiles
	// map so that subsequent store-path operations and the TUI use the correct
	// folder layout:
	//   .connext/<serial>/<domain_template_id>/<participant_template_id>/
	// Re-keying is necessary because getOrCreateProfile created the profile
	// under a temporary key (serviceID/participantID) before domainTemplateID
	// was known.  After this point two separate enrollments with the same
	// participantID but different domainTemplateIDs will have distinct keys.
	if domainTemplateID != "" {
		oldKey := profileKey(req.ServiceID, req.ParticipantID, req.DeviceName)
		p.mu.Lock()
		p.domainTemplateID = domainTemplateID
		p.mu.Unlock()
		newKey := profileKey(p.effectiveDomainID(), req.ParticipantID, req.DeviceName)
		if oldKey != newKey {
			a.profiles.Delete(oldKey)
			a.profiles.Store(newKey, p)
		}
	}

	p.mu.Lock()
	p.setState(StateEnrolled)
	p.deviceName = req.DeviceName
	p.mu.Unlock()

	// Compute the authoritative output path now that domainTemplateID is known.
	output := a.Store.ConnextArtifactsDir(p.serial, p.effectiveDomainID(), storePart) + string(os.PathSeparator)

	// Derive and persist the device endpoint URL so that subsequent mTLS
	// calls (identity, permissions, etc.) can resolve it from the store.
	url := a.Store.ResolveDeviceURL(p.serial, p.effectiveDomainID(), storePart)
	if url == "" && a.DeriveDeviceURLFunc != nil {
		url = a.DeriveDeviceURLFunc(req.ServiceID)
		if url != "" {
			_ = a.Store.WriteDeviceURL(p.serial, p.effectiveDomainID(), storePart, url)
		}
	}
	if url == "" {
		p.mu.Lock()
		p.setState(StateUnregistered)
		p.mu.Unlock()
		return fmt.Errorf("cannot determine device endpoint URL; configure a region with 'rticloud configure --region <region>'")
	}
	cert, key, ca := a.Store.ResolveMTLSDefaults(p.serial, p.effectiveDomainID(), storePart, "", "", "")

	notAfterIdentity, err := a.renewIdentity(p, url, cert, key, ca, output)
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}

	if err := a.RequestPermissionsFunc(url, cert, key, ca, "", req.ParticipantID, output); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	if err := a.RequestPSKFunc(url, cert, key, ca, "", output); err != nil {
		return fmt.Errorf("psk: %w", err)
	}
	if err := a.GetCRLFunc(url, cert, key, ca, "", req.ParticipantID, output); err != nil {
		return fmt.Errorf("crl: %w", err)
	}

	outputDir := strings.TrimSuffix(output, string(os.PathSeparator))
	enrolledAt := a.Now()

	// Set up the PSK rolling-key initial file layout and phase timers.
	// initializePSKFiles also populates p.notAfter[ArtifactPSK],
	// p.notAfter[ArtifactPSKRotate], p.notAfter[ArtifactPSKCleanup],
	// p.pskBNotAfter, and p.pskBaseTTL.
	a.initializePSKFiles(p, outputDir, enrolledAt)
	p.mu.Lock()
	if !notAfterIdentity.IsZero() {
		p.notAfter[ArtifactIdentity] = notAfterIdentity
		if nb, _ := a.readLease(filepath.Join(outputDir, "identity_lease.json")); !nb.IsZero() {
			p.issuedAt[ArtifactIdentity] = nb
		} else {
			p.issuedAt[ArtifactIdentity] = enrolledAt
		}
	}
	if nb, na := a.readLease(filepath.Join(outputDir, "permissions_lease.json")); !na.IsZero() {
		p.notAfter[ArtifactPermissions] = na
		if !nb.IsZero() {
			p.issuedAt[ArtifactPermissions] = nb
		} else {
			p.issuedAt[ArtifactPermissions] = enrolledAt
		}
	}
	// ArtifactPSK notAfter and the PSK phase timer entries (ArtifactPSKRotate,
	// ArtifactPSKCleanup) are set by initializePSKFiles above; no need to
	// re-read psk_lease.json here.
	// CRL has no server-side lease; refresh periodically.
	p.notAfter[ArtifactCRL] = enrolledAt.Add(a.CRLInterval)
	p.issuedAt[ArtifactCRL] = enrolledAt
	// Track device cert expiry for display (not yet renewable).
	if na := a.readCertNotAfter(a.Store.DeviceCertPath(p.serial, p.effectiveDomainID(), storePart)); !na.IsZero() {
		p.notAfter[ArtifactDeviceCert] = na
		p.issuedAt[ArtifactDeviceCert] = enrolledAt
	}
	p.setState(StateActive)
	p.mu.Unlock()

	if err := a.persistState(p); err != nil {
		_, _ = fmt.Fprintf(a.Out, "Warning: could not persist agent state: %v\n", err)
	}
	a.scheduleAll(p)
	return nil
}

// getOrCreateProfile returns the existing in-memory profile or creates a new empty one.
func (a *Agent) getOrCreateProfile(serviceID, participantID, deviceName string) *profile {
	key := profileKey(serviceID, participantID, deviceName)
	if val, ok := a.profiles.Load(key); ok {
		return val.(*profile)
	}
	p := &profile{
		serviceID:     serviceID,
		participantID: participantID,
		deviceName:    deviceName,
		state:         StateUnregistered,
		notAfter:      make(map[ArtifactID]time.Time),
		issuedAt:      make(map[ArtifactID]time.Time),
		timers:        make(map[ArtifactID]*time.Timer),
	}
	a.profiles.Store(key, p)
	return p
}

// moveInboxFile moves a processed or failed inbox file to targetDir, writing
// an optional result sidecar when resultMsg is non-empty.
func (a *Agent) moveInboxFile(src, targetDir, resultMsg string) {
	if err := a.MkdirAll(targetDir, 0o755); err != nil {
		_, _ = fmt.Fprintf(a.Out, "Warning: inbox could not create target dir %s: %v\n", targetDir, err)
		return
	}
	dst := filepath.Join(targetDir, filepath.Base(src))
	if err := a.Rename(src, dst); err != nil {
		_, _ = fmt.Fprintf(a.Out, "Warning: inbox could not move file %s → %s: %v\n", src, dst, err)
		return
	}
	if resultMsg != "" {
		result := map[string]string{"error": resultMsg}
		data, _ := json.MarshalIndent(result, "", "  ")
		_ = a.WriteFile(dst+".result.json", append(data, '\n'), 0o644)
	}
}

// Clean removes all agent state from the store directory, resetting the agent
// for a fresh first-run experience.
func (a *Agent) Clean() error {
	return os.RemoveAll(a.Store.BaseDir)
}

// timestampWriter prepends a timestamp to each line written to the underlying writer.
type timestampWriter struct {
	w io.Writer
}

func (tw timestampWriter) Write(p []byte) (int, error) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z ")
	lines := strings.Split(string(p), "\n")
	for i, line := range lines {
		if line == "" && i == len(lines)-1 {
			break // skip trailing empty line from split
		}
		if _, err := fmt.Fprintf(tw.w, "%s%s\n", ts, line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
