// Package edgesyncagent implements the long-lived artifact lifecycle manager
// for rticloud edge-sync agent.
package edgesyncagent

import (
	"context"
	"encoding/json"
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
	EnrollFunc             func(serviceID, participantID, serial string, macs []string, csrFile, keyFile, campaignToken string) (string, error)
	RequestIdentityFunc    func(url, cert, key, ca, serverAddr, csrFile, output string) error
	RequestPermissionsFunc func(url, cert, key, ca, serverAddr, output string) error
	RequestPSKFunc         func(url, cert, key, ca, serverAddr, output string) error
	GetCRLFunc             func(url, cert, key, ca, serverAddr, output string) error
	RenewDeviceCertFunc    func(url, cert, key, ca, serverAddr, csrFile string, validityMinutes int, output string) error
	GenerateKeyAndCSRFunc  func(commonName, org, tmpDir string) (keyPath, csrPath string, err error)
	GenerateCSRFromKeyFunc func(commonName, org string, keyPEM []byte, tmpDir string) (csrPath string, err error)

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

	// DeviceID, when non-empty, is used as the serial/device identifier
	// without prompting or auto-detecting.
	DeviceID string

	// MACs, when non-empty, is used as the MAC address list without
	// prompting or auto-detecting.
	MACs []string

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
// Layout (serial-rooted):
//
//	.connext/<serial>/<domain_template_id>/<participant_id>/agent_state.json            (no device name)
//	.connext/<serial>/<domain_template_id>/<participant_id>/<device_name>/agent_state.json
func (a *Agent) rehydrate() {
	serials, err := a.ReadDir(a.Store.BaseDir)
	if err != nil {
		return
	}
	for _, serial := range serials {
		if !serial.IsDir() {
			continue
		}
		serialPath := filepath.Join(a.Store.BaseDir, serial.Name())
		domains, err := a.ReadDir(serialPath)
		if err != nil {
			continue
		}
		for _, domain := range domains {
			if !domain.IsDir() {
				continue
			}
			domainPath := filepath.Join(serialPath, domain.Name())
			participants, err := a.ReadDir(domainPath)
			if err != nil {
				continue
			}
			for _, participant := range participants {
				if !participant.IsDir() {
					continue
				}
				participantPath := filepath.Join(domainPath, participant.Name())

				// No-device slot: serial/domain/participant/agent_state.json
				if _, err := a.ReadFile(filepath.Join(participantPath, "agent_state.json")); err == nil {
					a.loadProfile(serial.Name(), domain.Name(), participant.Name(), "")
					continue
				}

				// With-device slot: serial/domain/participant/<device>/agent_state.json
				devices, err := a.ReadDir(participantPath)
				if err != nil {
					continue
				}
				for _, device := range devices {
					if !device.IsDir() {
						continue
					}
					if _, err := a.ReadFile(filepath.Join(participantPath, device.Name(), "agent_state.json")); err == nil {
						a.loadProfile(serial.Name(), domain.Name(), participant.Name(), device.Name())
					}
				}
			}
		}
	}
}

// loadProfile loads persisted state for one profile and schedules its timers.
// serial is the root folder (.connext/<serial>/).
// dirName is the domain_template_id.
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

	// Restore serviceID.  It is stored explicitly in current state files; older
	// state files written before the field existed fall back to the directory
	// name, which equalled the serviceID at that time.
	serviceID := st.ServiceID
	if serviceID == "" {
		serviceID = dirName
	}
	domainTemplateID := st.DomainTemplateID

	// Restore serial.  It is stored explicitly in current state files; fall back
	// to the on-disk root folder name when absent.
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
	// relocation.
	storePart := deviceSlot(req.ParticipantID, req.DeviceName)

	domainTemplateID, enrollErr := a.EnrollFunc(req.ServiceID, storePart, req.Serial, req.MACs, csrPath, keyPath, req.CampaignToken)
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
	// Priority: (1) already stored, (2) device_domain from campaign token.
	url := a.Store.ResolveDeviceURL(p.serial, p.effectiveDomainID(), storePart)
	if url == "" {
		if domain := CampaignTokenDeviceDomain(req.CampaignToken); domain != "" {
			url = "https://" + domain
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

	if err := a.RequestPermissionsFunc(url, cert, key, ca, "", output); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}
	if err := a.RequestPSKFunc(url, cert, key, ca, "", output); err != nil {
		return fmt.Errorf("psk: %w", err)
	}
	if err := a.GetCRLFunc(url, cert, key, ca, "", output); err != nil {
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
