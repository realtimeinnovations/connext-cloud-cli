// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

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
	"sync/atomic"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

const renewalThreshold = 0.80 // renew at 80% of artifact lifetime

// agentLogRingSize is the number of recent log lines retained in memory for the
// TUI "Agent Log" panel.  Only a slice of these is shown at once, depending on
// available terminal height.
const agentLogRingSize = 200

// ProfileState represents the lifecycle state of a Participant Profile.
type ProfileState string

const (
	StateUnregistered ProfileState = "unregistered"
	StateEnrolling    ProfileState = "enrolling"
	StateEnrolled     ProfileState = "enrolled"
	StateActive       ProfileState = "active"
	StateRenewing     ProfileState = "renewing"
	// StateRevoked marks a profile whose mTLS credentials were rejected by the
	// Provisioning Service (e.g. the participant was revoked from the webapp).
	// Set by renewArtifact on an auth-rejection error and cleared automatically
	// the moment any renewal for that profile succeeds again.
	StateRevoked ProfileState = "revoked"
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
//
// Two enrollment modes are supported: campaign enrollment (CampaignToken set,
// authenticated by the campaign JWT) and operator-initiated direct enrollment
// (DomainTemplateID set, authenticated with the operator's management token).
type EnrollRequest struct {
	ServiceID     string   `json:"service_id"`
	ParticipantID string   `json:"participant_id"`
	CampaignToken string   `json:"campaign_token,omitempty"`
	Serial        string   `json:"serial"`
	MACs          []string `json:"macs"`
	DeviceName    string   `json:"device_name"`

	// DomainTemplateID selects the domain template for direct enrollment.
	// When set (and CampaignToken is empty) the request is processed via
	// EnrollDirectFunc; the server may still override the value in its
	// response.
	DomainTemplateID string `json:"domain_template_id,omitempty"`
}

// AgentState is the on-disk representation of a profile's artifact state.
// Written to <slot>/agent_state.json after each successful fetch or renewal.
type AgentState struct {
	State      ProfileState             `json:"state"`
	NotAfter   map[ArtifactID]time.Time `json:"not_after"`
	IssuedAt   map[ArtifactID]time.Time `json:"issued_at,omitempty"`
	DeviceName string                   `json:"device_name,omitempty"`

	// ServiceID is the edge provisioning service (e.g. ces-alpha-123) — the
	// top level of the layered artifact tree. Used for CSR generation, API
	// endpoint URL construction, and the connext_artifacts/<service> root.
	ServiceID string `json:"service_id,omitempty"`

	// DomainTemplateID is the domain template (e.g. 0:domain-0849), the second
	// scope level under the service. Every enrolled node has one; rehydrate
	// reconstructs the profile from this field, so it is always set.
	DomainTemplateID string `json:"domain_template_id,omitempty"`

	// Serial is the device serial number — the node id level of the layered
	// layout (<service>/<domain>/<node>/<participant>), nested directly under
	// the domain so every participant template for one node lives together.
	Serial string `json:"serial,omitempty"`

	// ParticipantTemplateID is the participant template identifier — the leaf
	// scope level beneath the node serial. It matches the participant_id field
	// of the EnrollRequest.
	ParticipantTemplateID string `json:"participant_template_id,omitempty"`

	// PSK rolling-key protocol state.
	// PSKBNotAfter is the expiry of the "B" slot PSK obtained at enrollment
	// (or the most recently fetched sC at a prior 80% phase).  It is used to
	// schedule the PSKRotate and PSKCleanup timers for the next cycle.
	PSKBNotAfter time.Time `json:"psk_b_not_after,omitempty"`
	// PSKBNotBefore is the validity start of the "B" slot PSK.  pskRotate uses
	// it to anchor issuedAt[ArtifactPSK] to sB's real window (so the post-
	// rotation 80% renewal fires at 80% of sB's lifetime, not of "now").
	PSKBNotBefore time.Time `json:"psk_b_not_before,omitempty"`
	// PSKBaseTTL is the duration of sA's original lease (psk_a.notAfter −
	// issuedAt) and is used to compute the PSKCleanup fire-time (120% mark).
	PSKBaseTTL time.Duration `json:"psk_base_ttl,omitempty"`
}

// profile is the in-memory state for one serial+domain_template+participant triple.
type profile struct {
	mu               sync.Mutex
	serviceID        string // provisioning service / CSR org (e.g. ces-alpha-123); artifact-tree root
	domainTemplateID string // domain template (e.g. 0:domain-0849); domain scope
	serial           string // device serial; node id leaf
	participantID    string // participant template ID (request field); participant scope
	deviceName       string
	state            ProfileState
	notAfter         map[ArtifactID]time.Time
	issuedAt         map[ArtifactID]time.Time
	timers           map[ArtifactID]*time.Timer

	// PSK rolling-key protocol state (see AgentState for field semantics).
	pskBNotAfter  time.Time
	pskBNotBefore time.Time
	pskBaseTTL    time.Duration

	// lastSweepState is the profile state last surfaced to the Agent Log panel
	// by a sweep, so unchanged per-tick sweeps can be downgraded to file-only.
	lastSweepState ProfileState
}

func (p *profile) setState(s ProfileState) { p.state = s }

// ─── Layered-layout identifiers ──────────────────────────────────────────────
// These map the profile onto the (service, domain, participant, node) model
// used by the layered store paths.

// service is the provisioning service id (top level of the artifact tree).
func (p *profile) service() string { return p.serviceID }

// domain is the domain template id. Every enrolled node has one (enrollment
// fails if the service does not return a domain_template_id), so there is no
// fallback.
func (p *profile) domain() string { return p.domainTemplateID }

// participant is the participant template id.
func (p *profile) participant() string { return p.participantID }

// node is the per-node leaf id. The device serial uniquely identifies the
// node; deviceName is retained on the profile for in-memory keying and display
// but is not part of the on-disk path.
func (p *profile) node() string { return p.serial }

// ─── Domain-scoped artifact ownership ────────────────────────────────────────
// PSK and CRL (and the PSK phase events) are shared by every participant in a
// (service, domain): their on-disk state lives in the shared domain directory
// (psk_secret*.key, psk_secret.lease.json, crl.pem).  To avoid redundant fetches and, more
// importantly, concurrent corruption of the PSK rolling-key files, exactly one
// profile per (service, domain) — the "domain owner" — fetches, schedules and
// renews them.  Every profile still manages its own node-scoped artifacts
// (identity, permissions, device certificate).

// isDomainArtifact reports whether an artifact is managed once per domain by
// the domain owner rather than once per node.
func isDomainArtifact(id ArtifactID) bool {
	switch id {
	case ArtifactPSK, ArtifactCRL, ArtifactPSKRotate, ArtifactPSKCleanup:
		return true
	}
	return false
}

// domainOwnerKey identifies the (service, domain) whose domain-scoped artifacts
// are managed by a single owner profile.
func domainOwnerKey(service, domain string) string { return service + "/" + domain }

// claimDomainOwner records p as the owner of its (service, domain).  Ownership
// prefers the profile that already holds the domain state (a non-zero PSK
// expiry) so that, after a restart, the profile that originally fetched the PSK
// reclaims ownership regardless of rehydration order.  Callers must not hold
// p.mu; it is only invoked from the serial enroll and rehydrate paths.
//
// A revoked profile is never preferred over a non-revoked one, even if it
// still carries stale domain state from before it was revoked: without this
// check, restarting the agent after renewArtifact's auth-rejection failover
// (see failoverDomainOwner) could re-elect the revoked participant as owner
// again, purely because its on-disk state happened to load first.
func (a *Agent) claimDomainOwner(p *profile) {
	key := domainOwnerKey(p.service(), p.domain())
	existing, loaded := a.domainOwners.LoadOrStore(key, p)
	if !loaded {
		return
	}
	cur := existing.(*profile)
	if cur == p {
		return
	}
	if a.isRevoked(cur) && !a.isRevoked(p) {
		a.domainOwners.Store(key, p)
		return
	}
	if a.isRevoked(p) {
		return
	}
	if a.hasDomainState(p) && !a.hasDomainState(cur) {
		a.domainOwners.Store(key, p)
	}
}

// isDomainOwner reports whether p owns the domain-scoped artifacts for its
// (service, domain).
func (a *Agent) isDomainOwner(p *profile) bool {
	v, ok := a.domainOwners.Load(domainOwnerKey(p.service(), p.domain()))
	return ok && v.(*profile) == p
}

// hasDomainState reports whether p carries live domain state (a known PSK
// expiry), used to prefer the original fetcher as owner across restarts.
func (a *Agent) hasDomainState(p *profile) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.notAfter[ArtifactPSK].IsZero()
}

// isRevoked reports whether p's mTLS credentials were last rejected by the
// Provisioning Service (see renewArtifact's auth-rejection handling).
func (a *Agent) isRevoked(p *profile) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == StateRevoked
}

// profileKey uniquely identifies a profile in the in-memory map. The first
// segment is the profile's domain template id (p.domain()) once enrolled;
// getOrCreateProfile keys the transient pre-enrollment profile by serviceID
// until the domain is known, after which enrollProfile re-keys it, so two
// enrollments with the same participant but different domain templates stay
// distinct.
//
// The serial (node id) is the terminal segment: multiple nodes can share the
// same domain and participant template (a fleet enrolled from one template),
// and each is a distinct profile. deviceName is a display-only label and is
// deliberately NOT part of the key.
func profileKey(domainOrService, participantID, serial string) string {
	return domainOrService + "/" + participantID + "/" + serial
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
	EnrollFunc func(serviceID, participantID, serial string, macs []string, csrFile, keyFile, campaignToken string) (string, error)
	// EnrollDirectFunc calls the operator-initiated direct enrollment API
	// (management token, no campaign JWT).  It returns the domain_template_id
	// confirmed by the server and the device endpoint URL (nodeUrl) from the
	// enrollment response.
	EnrollDirectFunc func(serviceID, domainTemplateID, participantTemplateID, serial string, macs []string, deviceName, csrFile, keyFile string) (string, string, error)
	// Catalogue lookups for the operator enrollment wizard (require a
	// logged-in management token; the first call triggers the login flow when
	// no session exists).  Wired by NewRuntime.
	ListServicesFunc             func() ([]string, error)
	ListDomainTemplatesFunc      func(service string) ([]string, error)
	ListParticipantTemplatesFunc func(service string) ([]string, error)

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
	RemoveFile func(string) error

	// Configuration.
	LogFile       string // Path to agent log file; created/appended by Run.
	InboxDir      string
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

	// DeploymentName, when non-empty, is used as the serial/device identifier
	// without prompting or auto-detecting.
	DeploymentName string

	// MACs, when non-empty, is used as the MAC address list without
	// prompting or auto-detecting.
	MACs []string

	// CampaignToken, when non-empty, enrolls the first profile with this
	// campaign token without prompting (headless campaign enrollment).
	CampaignToken string

	// Service, DomainTemplateID and ParticipantTemplateID pre-answer the
	// operator wizard questions (each set value skips its pick-list).  When
	// all three are set, first-run enrollment proceeds without any prompting
	// (headless direct enrollment).
	Service               string
	DomainTemplateID      string
	ParticipantTemplateID string

	// Internal state.
	termOut      io.Writer          // original terminal writer for TUI rendering
	stopFunc     context.CancelFunc // cancels the agent's context (Ctrl+C from TUI)
	profiles     sync.Map           // profileKey → *profile
	domainOwners sync.Map           // "service/domain" → *profile that manages the domain-scoped artifacts (PSK, CRL)
	wg           sync.WaitGroup
	outMu        sync.Mutex  // serializes writes to Out/LogOut/event sinks across goroutines
	logs         *logRing    // recent log events surfaced in the TUI "Agent Log" panel
	tuiActive    atomic.Bool // true while the live TUI is painting the terminal

	// Raw (unwrapped) sinks for emit, written under outMu. eventStdout reaches
	// stdout+file; eventFile reaches the file only. They are the pre-syncWriter
	// writers so emit can serialize via outMu without re-entering syncWriter.
	eventStdout io.Writer
	eventFile   io.Writer
}

// logEntry is one buffered Agent Log line. When classified is true, sev is the
// authoritative severity supplied by the emitting event (see Agent.emit); when
// false the renderer falls back to keyword classification (agentLogSeverity).
// t is the arrival time, used to show a leading timestamp in the panel.
type logEntry struct {
	text       string
	sev        tui.LogSeverity
	classified bool
	t          time.Time
}

// logRing is a thread-safe, fixed-capacity ring buffer of the most recent agent
// log entries.  It implements io.Writer so external diagnostics (e.g. raw HTTP
// debug output) can still be teed in; those arrive unclassified.
type logRing struct {
	mu      sync.Mutex
	entries []logEntry
	max     int
}

func newLogRing(max int) *logRing {
	if max <= 0 {
		max = agentLogRingSize
	}
	return &logRing{max: max, entries: make([]logEntry, 0, max)}
}

// Write captures each newline-terminated line (io.Writer). Lines arriving this
// way are unclassified and fall back to keyword severity at render time. Blank
// lines are dropped.
func (r *logRing) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		r.appendEntry(logEntry{text: line, t: time.Now()})
	}
	return len(p), nil
}

// append adds an unclassified line (used by tests and direct callers).
func (r *logRing) append(line string) {
	r.appendEntry(logEntry{text: line})
}

// appendEvent adds a line with the authoritative severity and arrival time from
// Agent.emit.
func (r *logRing) appendEvent(line string, sev tui.LogSeverity, t time.Time) {
	r.appendEntry(logEntry{text: line, sev: sev, classified: true, t: t})
}

func (r *logRing) appendEntry(e logEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	if len(r.entries) > r.max {
		r.entries = append([]logEntry(nil), r.entries[len(r.entries)-r.max:]...)
	}
}

// recent returns the buffered line texts, oldest first.
func (r *logRing) recent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.text
	}
	return out
}

// recentEntries returns a copy of the buffered entries, oldest first.
func (r *logRing) recentEntries() []logEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]logEntry(nil), r.entries...)
}

// clockNow returns the agent's time source, defaulting to time.Now when Now is
// unset (e.g. an Agent built directly in a unit test).
func (a *Agent) clockNow() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// syncWriter serializes concurrent writes to an underlying writer using a
// shared mutex.  The sweep and inbox loops run on separate goroutines and both
// write diagnostics to Out/LogOut; wrapping those writers keeps the writes from
// racing or interleaving.
type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
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
		RemoveFile:    os.Remove,
		InboxDir:      store.InboxDir(),
		PollInterval:  10 * time.Second,
		SweepInterval: 5 * time.Minute,
		CRLInterval:   5 * time.Minute,
		RetryInterval: 2 * time.Minute,
		logs:          newLogRing(agentLogRingSize),
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

	// Open log file — tee terminal output to the file too.  Agent lifecycle
	// events flow through a.emit, which appends to the in-memory ring buffer
	// (TUI "Agent Log" panel) and writes to the file sink; LogOut is therefore
	// just the timestamped file sink (no longer teed to the ring).
	if a.logs == nil {
		a.logs = newLogRing(agentLogRingSize)
	}
	var fileSink io.Writer = io.Discard
	if a.LogFile != "" {
		if err := a.MkdirAll(filepath.Dir(a.LogFile), 0o755); err != nil {
			return fmt.Errorf("creating log directory: %w", err)
		}
		lf, err := os.OpenFile(a.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("opening log file %s: %w", a.LogFile, err)
		}
		defer lf.Close()
		fileSink = timestampWriter{w: lf}
		a.Out = io.MultiWriter(a.Out, fileSink)
	}
	a.LogOut = fileSink

	// Raw sinks for emit (written under outMu, never via syncWriter): events go
	// to stdout+file when no TUI paints the terminal, file-only while it does.
	a.eventStdout = a.Out
	a.eventFile = fileSink

	// Serialize all writes to Out/LogOut through one mutex: the sweep and inbox
	// loops below run concurrently and both emit diagnostics. termOut is left
	// unwrapped so TUI rendering and terminal-type detection are unaffected.
	a.Out = &syncWriter{mu: &a.outMu, w: a.Out}
	a.LogOut = &syncWriter{mu: &a.outMu, w: a.LogOut}

	if err := a.MkdirAll(a.InboxDir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", a.InboxDir, err)
	}

	a.rehydrate()
	a.drainInbox()

	// Reuse enrollments performed out-of-band (`rticloud edge-provisioning
	// enroll`/`enroll-direct`), which leave mTLS credentials on disk with no
	// agent_state.json. When the agent already manages a profile, silently
	// adopt any such nodes (Option A). When it manages none, the first-run
	// wizard offers to reuse them interactively before enrolling afresh
	// (Option B, see ConfigureFirstRun).
	if a.countProfiles() == 0 {
		if err := a.ConfigureFirstRun(ctx); err != nil {
			return err
		}
	} else {
		a.adoptExistingEnrollments()
	}

	a.wg.Add(2)
	go a.sweepLoop(ctx)
	go a.inboxLoop(ctx)

	a.runDisplay(ctx) // blocks until ctx is cancelled
	a.wg.Wait()
	_, _ = fmt.Fprint(a.Out, "\nEdge-Sync Agent interrupted.\n")
	if a.LogFile != "" {
		_, _ = fmt.Fprintf(a.Out, "• Logs saved under %s\n", filepath.Dir(a.LogFile))
	}
	_, _ = fmt.Fprintln(a.Out, "• Run 'rticloud agent' from this directory to start this agent again.")
	return nil
}

// ─── Startup rehydration ─────────────────────────────────────────────────────

// rehydrate walks the per-node agent tree looking for agent_state.json files
// and rebuilds the in-memory profile map and renewal timers without contacting
// the service.  Each state file is self-describing (it carries service, domain,
// participant, serial and device name), so the profile is reconstructed from
// the file contents rather than from its on-disk location.
//
// Layout:
//
//	<agent>/<service>/mtls_artifacts/<domain>/<node>/<participant>/agent_state.json
func (a *Agent) rehydrate() {
	for _, statePath := range a.findStateFiles(a.Store.AgentDir()) {
		a.loadProfile(statePath)
	}
}

// findNodeDomain searches the mTLS tree for an already-stored node key under
// the given service and returns the domain template id it lives in, or "" when
// no such node is found.  Used to resume a 409 (already-enrolled) response,
// which does not carry the domain template, without guessing the path.
func (a *Agent) findNodeDomain(service, participant, node string) string {
	domains, err := a.ReadDir(a.Store.MTLSRoot(service))
	if err != nil {
		return ""
	}
	for _, d := range domains {
		if !d.IsDir() {
			continue
		}
		if _, err := a.ReadFile(a.Store.NodeKeyPath(service, d.Name(), participant, node)); err == nil {
			return edgestore.DomainFromPathSegment(d.Name())
		}
	}
	return ""
}

// findStateFiles returns every agent_state.json path under dir (recursively).
func (a *Agent) findStateFiles(dir string) []string {
	entries, err := a.ReadDir(dir)
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			found = append(found, a.findStateFiles(path)...)
		} else if entry.Name() == "agent_state.json" {
			found = append(found, path)
		}
	}
	return found
}

// loadProfile loads persisted state from statePath and schedules its timers.
// All identifiers are read from the (self-describing) state file.
func (a *Agent) loadProfile(statePath string) {
	data, err := a.ReadFile(statePath)
	if err != nil {
		return
	}
	var st AgentState
	if err := json.Unmarshal(data, &st); err != nil {
		a.emitf(catWarning, tui.LogWarn, "Warning: corrupt agent state file %s, skipping: %v", statePath, err)
		return
	}

	p := &profile{
		serviceID:        st.ServiceID,
		domainTemplateID: st.DomainTemplateID,
		serial:           st.Serial,
		participantID:    st.ParticipantTemplateID,
		deviceName:       st.DeviceName,
		state:            st.State,
		notAfter:         st.NotAfter,
		issuedAt:         st.IssuedAt,
		timers:           make(map[ArtifactID]*time.Timer),
		pskBNotAfter:     st.PSKBNotAfter,
		pskBNotBefore:    st.PSKBNotBefore,
		pskBaseTTL:       st.PSKBaseTTL,
	}
	if p.notAfter == nil {
		p.notAfter = make(map[ArtifactID]time.Time)
	}
	if p.issuedAt == nil {
		p.issuedAt = make(map[ArtifactID]time.Time)
	}
	a.profiles.Store(profileKey(p.domain(), p.participantID, p.serial), p)
	a.scheduleAll(p)
	a.emitf(catState, tui.LogInfo, "profile rehydrated serial=%s service=%s domain=%s participant=%s device=%s state=%s",
		p.serial, p.serviceID, p.domain(), p.participantID, p.deviceName, p.state)
}

// ─── Adoption of out-of-band enrollments ─────────────────────────────────────

// adoptableNode describes a node whose mTLS credentials are already on disk
// (from a standalone `rticloud edge-provisioning enroll`/`enroll-direct`) but
// which the agent has never managed itself, i.e. it has a node.key and a
// node_url but no agent_state.json. Such a node can be adopted: the agent skips
// enrollment and runs the artifact-fetch sequence against the existing
// credentials.
type adoptableNode struct {
	service     string
	domain      string
	participant string
	node        string
	url         string
}

// countProfiles returns the number of in-memory profiles.
func (a *Agent) countProfiles() int {
	n := 0
	a.profiles.Range(func(_, _ any) bool { n++; return true })
	return n
}

// findAdoptableNodes walks the per-node mTLS tree
// (<agent>/<service>/mtls_artifacts/<domain>/<node>/<participant>) and returns
// every node that carries mTLS credentials (node.key) and a stored device
// endpoint URL (node_url) but has neither an agent_state.json nor an already
// loaded in-memory profile. These are enrollments performed out-of-band that
// the agent can reuse instead of enrolling afresh.
//
// The walk uses the agent's injectable file operations (like findStateFiles)
// so it stays consistent with rehydrate and remains testable with an in-memory
// filesystem.
func (a *Agent) findAdoptableNodes() []adoptableNode {
	var found []adoptableNode
	services, err := a.ReadDir(a.Store.AgentDir())
	if err != nil {
		return nil
	}
	for _, svc := range services {
		if !svc.IsDir() || svc.Name() == "inbox" {
			continue
		}
		service := svc.Name()
		domains, err := a.ReadDir(a.Store.MTLSRoot(service))
		if err != nil {
			continue
		}
		for _, dom := range domains {
			if !dom.IsDir() {
				continue
			}
			// dom.Name() is the on-disk (path-sanitized) directory name; recover
			// the true "<id>:<tag>" domain template id for use as a profile key
			// and for anything that leaves the filesystem (state, display, API).
			domain := edgestore.DomainFromPathSegment(dom.Name())
			nodeDirs, err := a.ReadDir(filepath.Join(a.Store.MTLSRoot(service), dom.Name()))
			if err != nil {
				continue
			}
			for _, nd := range nodeDirs {
				if !nd.IsDir() {
					continue
				}
				node := nd.Name()
				parts, err := a.ReadDir(filepath.Join(a.Store.MTLSRoot(service), dom.Name(), node))
				if err != nil {
					continue
				}
				for _, part := range parts {
					if !part.IsDir() {
						continue
					}
					participant := part.Name()
					if n, ok := a.adoptableNode(service, domain, participant, node); ok {
						found = append(found, n)
					}
				}
			}
		}
	}
	return found
}

// adoptableNode reports whether the given node slot is eligible for adoption
// and, if so, returns its descriptor. A slot is adoptable when it holds mTLS
// credentials (node.key) and a non-empty node_url, but no agent_state.json and
// no in-memory profile keyed to the same (domain, participant) slot.
func (a *Agent) adoptableNode(service, domain, participant, node string) (adoptableNode, bool) {
	// An existing agent_state.json means the node was (or will be) rehydrated;
	// never adopt over managed state.
	if _, err := a.ReadFile(a.Store.NodeStatePath(service, domain, participant, node)); err == nil {
		return adoptableNode{}, false
	}
	if _, err := a.ReadFile(a.Store.NodeKeyPath(service, domain, participant, node)); err != nil {
		return adoptableNode{}, false
	}
	data, err := a.ReadFile(a.Store.NodeURLPath(service, domain, participant, node))
	if err != nil {
		return adoptableNode{}, false
	}
	url := strings.TrimSpace(string(data))
	if url == "" {
		return adoptableNode{}, false
	}
	if _, ok := a.profiles.Load(profileKey(domain, participant, node)); ok {
		return adoptableNode{}, false
	}
	return adoptableNode{service: service, domain: domain, participant: participant, node: node, url: url}, true
}

// adoptProfile builds a profile from an adoptable node's on-disk credentials
// and runs the artifact-fetch sequence against them, reusing the existing
// enrollment instead of enrolling a new device. On failure the half-built
// profile is removed so a later run can retry (or fall back to enrollment).
func (a *Agent) adoptProfile(n adoptableNode) error {
	key := profileKey(n.domain, n.participant, n.node)
	if _, ok := a.profiles.Load(key); ok {
		return nil // already managed
	}
	p := &profile{
		serviceID:        n.service,
		domainTemplateID: n.domain,
		serial:           n.node,
		participantID:    n.participant,
		state:            StateEnrolled,
		notAfter:         make(map[ArtifactID]time.Time),
		issuedAt:         make(map[ArtifactID]time.Time),
		timers:           make(map[ArtifactID]*time.Timer),
	}
	a.profiles.Store(key, p)
	a.emitf(catEnroll, tui.LogInfo, "adopting existing enrollment service=%s domain=%s participant=%s serial=%s",
		n.service, n.domain, n.participant, n.node)
	if err := a.fetchAndActivate(p, "", n.url); err != nil {
		a.profiles.Delete(key)
		return err
	}
	return nil
}

// adoptExistingEnrollments adopts every out-of-band enrollment found on disk
// (Option A: silent auto-adoption). It is best-effort: a failure to adopt one
// node is logged and does not abort the others or agent startup. Used when the
// agent already manages at least one profile, where discovering additional
// pre-enrolled nodes should simply pull them in without prompting.
func (a *Agent) adoptExistingEnrollments() {
	for _, n := range a.findAdoptableNodes() {
		if err := a.adoptProfile(n); err != nil {
			a.emitf(catWarning, tui.LogWarn,
				"Warning: could not adopt existing enrollment service=%s domain=%s participant=%s serial=%s: %v",
				n.service, n.domain, n.participant, n.node, err)
		}
	}
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
		owner := a.isDomainOwner(p)
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, artifact := range allArtifacts {
			// Domain-scoped artifacts are renewed only by the domain owner.
			if isDomainArtifact(artifact) && !owner {
				continue
			}
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
		// Sweep PSK phase timers (exact-time single-shot events) for the owner.
		if owner {
			a.schedulePSKPhasesLocked(p, now)
		}
		// Routine per-tick sweeps go to the file only; surface in the panel just
		// when the profile state changed since the last sweep.
		stateChanged := p.lastSweepState != p.state
		p.lastSweepState = p.state
		a.emit(agentEvent{
			cat:      catSweep,
			sev:      tui.LogInfo,
			detail:   fmt.Sprintf("sweep status service=%s participant=%s state=%s", p.serviceID, p.participantID, p.state),
			fileOnly: !stateChanged,
		})
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
		a.emitf(catInbox, tui.LogWarn, "inbox read failed path=%s err=%v", path, err)
		return
	}
	var req EnrollRequest
	if err := json.Unmarshal(data, &req); err != nil {
		a.emitf(catInbox, tui.LogWarn, "inbox invalid JSON path=%s err=%v", path, err)
		a.removeInboxFile(path)
		return
	}
	// MACs are required by the campaign enrollment endpoint; direct
	// (domain_template_id) requests may omit them.
	if req.ServiceID == "" || req.ParticipantID == "" || req.Serial == "" ||
		(req.DomainTemplateID == "" && len(req.MACs) == 0) {
		a.emitf(catInbox, tui.LogWarn, "inbox missing required fields (service_id, participant_id, serial, macs) path=%s", path)
		a.removeInboxFile(path)
		return
	}

	a.emitf(catInbox, tui.LogInfo, "inbox enrollment request received service=%s participant=%s",
		req.ServiceID, req.ParticipantID)

	if err := a.enrollProfile(req); err != nil {
		a.emitf(catInbox, tui.LogWarn, "inbox enrollment failed service=%s participant=%s err=%v",
			req.ServiceID, req.ParticipantID, err)
		a.removeInboxFile(path)
		return
	}
	a.removeInboxFile(path)
	a.emitf(catEnroll, tui.LogGood, "inbox enrollment complete service=%s participant=%s",
		req.ServiceID, req.ParticipantID)
}

// enrollProfile runs the full enrollment + artifact-fetch sequence for a new profile.
func (a *Agent) enrollProfile(req EnrollRequest) error {
	p := a.getOrCreateProfile(req.ServiceID, req.ParticipantID, req.Serial, req.DeviceName)

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

	// The participant template and serial (node) are passed separately so
	// EnrollDevice writes directly into the layered node slot.  Direct
	// (operator-initiated) requests carry a domain template instead of a
	// campaign token and go through the enroll-node endpoint, which also
	// returns the device endpoint URL.
	var domainTemplateID, directNodeURL string
	var enrollErr error
	if req.DomainTemplateID != "" && req.CampaignToken == "" {
		if a.EnrollDirectFunc == nil {
			p.mu.Lock()
			p.setState(StateUnregistered)
			p.mu.Unlock()
			return fmt.Errorf("direct enrollment is not configured")
		}
		domainTemplateID, directNodeURL, enrollErr = a.EnrollDirectFunc(req.ServiceID, req.DomainTemplateID, req.ParticipantID, req.Serial, req.MACs, req.DeviceName, csrPath, keyPath)
	} else {
		domainTemplateID, enrollErr = a.EnrollFunc(req.ServiceID, req.ParticipantID, req.Serial, req.MACs, csrPath, keyPath, req.CampaignToken)
	}
	if enrollErr != nil {
		// HTTP 409 means the device is already enrolled in this campaign (e.g. a
		// previous attempt succeeded but a later step failed).  The 409 response
		// does not carry the domain template, so locate the already-stored node
		// in the layered store to recover its real domain and resume the fetch
		// using the existing credentials.
		alreadyEnrolled := strings.Contains(enrollErr.Error(), "409")
		if alreadyEnrolled {
			domainTemplateID = a.findNodeDomain(req.ServiceID, req.ParticipantID, req.Serial)
		}
		if !alreadyEnrolled || domainTemplateID == "" {
			p.mu.Lock()
			p.setState(StateUnregistered)
			p.mu.Unlock()
			return fmt.Errorf("enrollment: %w", enrollErr)
		}
		// Already enrolled: fall through using the stored credentials.
		a.emitf(catEnroll, tui.LogInfo, "Note: device already enrolled; resuming artifact fetch.")
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
		oldKey := profileKey(req.ServiceID, req.ParticipantID, req.Serial)
		p.mu.Lock()
		p.domainTemplateID = domainTemplateID
		p.mu.Unlock()
		newKey := profileKey(p.domain(), req.ParticipantID, req.Serial)
		if oldKey != newKey {
			a.profiles.Delete(oldKey)
			a.profiles.Store(newKey, p)
		}
	}

	p.mu.Lock()
	p.setState(StateEnrolled)
	p.deviceName = req.DeviceName
	p.mu.Unlock()

	return a.fetchAndActivate(p, req.CampaignToken, directNodeURL)
}

// fetchAndActivate runs the post-enrollment artifact-fetch sequence (identity,
// permissions and, for the domain owner, PSK and CRL) for a profile whose mTLS
// credentials are already present in the store, then records the artifact
// leases, persists agent_state.json and schedules the renewal timers.
//
// It is shared by two callers: enrollProfile (immediately after a successful
// enrollment) and adoptProfile (reusing an enrollment performed out-of-band by
// `rticloud edge-provisioning enroll`/`enroll-direct`). campaignToken and
// directNodeURL only influence how the device endpoint URL is resolved when the
// store has none yet; both are empty when adopting, where the node_url file is
// already on disk.
func (a *Agent) fetchAndActivate(p *profile, campaignToken, directNodeURL string) error {
	// Compute the scoped output paths now that domainTemplateID is known.
	// Identity and permissions are node-scoped; PSK and CRL are domain-scoped.
	service, domain, participant, node := p.service(), p.domain(), p.participant(), p.node()
	nodeDir := a.Store.NodeDir(service, domain, participant, node)
	domainDir := a.Store.DomainDir(service, domain)
	nodeOut := nodeDir + string(os.PathSeparator)
	domainOut := domainDir + string(os.PathSeparator)

	// Derive and persist the device endpoint URL so that subsequent mTLS
	// calls (identity, permissions, etc.) can resolve it from the store.
	// Priority: (1) already stored, (2) nodeUrl from the direct enrollment
	// response, (3) device_domain from campaign token.
	url := a.Store.ResolveNodeURL(service, domain, participant, node)
	if url == "" && directNodeURL != "" {
		url = directNodeURL
		_ = a.Store.WriteNodeURL(service, domain, participant, node, url)
	}
	if url == "" {
		if deviceDomain := CampaignTokenDeviceDomain(campaignToken); deviceDomain != "" {
			url = "https://" + deviceDomain
			_ = a.Store.WriteNodeURL(service, domain, participant, node, url)
		}
	}
	if url == "" {
		p.mu.Lock()
		p.setState(StateUnregistered)
		p.mu.Unlock()
		if campaignToken == "" {
			return fmt.Errorf("cannot determine device endpoint URL; the enrollment response did not include nodeUrl")
		}
		return fmt.Errorf("cannot determine device endpoint URL; configure a region with 'rticloud configure --region <region>'")
	}
	cert, key, ca := a.Store.ResolveNodeMTLS(service, domain, participant, node, "", "", "")

	notAfterIdentity, err := a.renewIdentity(p, url, cert, key, ca, nodeOut)
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}

	if err := a.RequestPermissionsFunc(url, cert, key, ca, "", nodeOut); err != nil {
		return fmt.Errorf("permissions: %w", err)
	}

	// PSK and CRL are domain-scoped: their files live in the shared domain
	// directory and must be fetched and managed by a single owner per
	// (service, domain). The first participant to enroll into a domain becomes
	// the owner; later participants reuse the owner's files.
	a.claimDomainOwner(p)
	owner := a.isDomainOwner(p)
	if owner {
		if err := a.RequestPSKFunc(url, cert, key, ca, "", domainOut); err != nil {
			return fmt.Errorf("psk: %w", err)
		}
		if err := a.GetCRLFunc(url, cert, key, ca, "", domainOut); err != nil {
			return fmt.Errorf("crl: %w", err)
		}
	}

	enrolledAt := a.Now()

	// Set up the PSK rolling-key initial file layout and phase timers (owner
	// only). initializePSKFiles populates p.notAfter[ArtifactPSK],
	// p.notAfter[ArtifactPSKRotate], p.notAfter[ArtifactPSKCleanup],
	// p.pskBNotAfter, and p.pskBaseTTL.
	if owner {
		a.initializePSKFiles(p, domainDir, enrolledAt)
	}
	p.mu.Lock()
	if !notAfterIdentity.IsZero() {
		p.notAfter[ArtifactIdentity] = notAfterIdentity
		if nb, _ := a.readLease(filepath.Join(nodeDir, "identity.lease.json")); !nb.IsZero() {
			p.issuedAt[ArtifactIdentity] = nb
		} else {
			p.issuedAt[ArtifactIdentity] = enrolledAt
		}
	}
	if nb, na := a.readLease(filepath.Join(nodeDir, "permissions.lease.json")); !na.IsZero() {
		p.notAfter[ArtifactPermissions] = na
		if !nb.IsZero() {
			p.issuedAt[ArtifactPermissions] = nb
		} else {
			p.issuedAt[ArtifactPermissions] = enrolledAt
		}
	}
	// ArtifactPSK notAfter and the PSK phase timer entries (ArtifactPSKRotate,
	// ArtifactPSKCleanup) are set by initializePSKFiles above (owner only); no
	// need to re-read psk_secret.lease.json here.
	// CRL has no server-side lease; refresh periodically (owner only).
	if owner {
		p.notAfter[ArtifactCRL] = enrolledAt.Add(a.CRLInterval)
		p.issuedAt[ArtifactCRL] = enrolledAt
	}
	// Track device cert expiry for display (not yet renewable).
	if na := a.readCertNotAfter(a.Store.NodeCertPath(service, domain, participant, node)); !na.IsZero() {
		p.notAfter[ArtifactDeviceCert] = na
		p.issuedAt[ArtifactDeviceCert] = enrolledAt
	}
	p.setState(StateActive)
	p.mu.Unlock()

	if err := a.persistState(p); err != nil {
		a.emitf(catWarning, tui.LogWarn, "Warning: could not persist agent state: %v", err)
	}
	a.scheduleAll(p)
	return nil
}

// getOrCreateProfile returns the existing in-memory profile or creates a new
// empty one. The profile is keyed by (serviceID, participantID, serial); serial
// is what distinguishes two nodes enrolled from the same participant template.
// deviceName is retained on the profile as a display-only label.
func (a *Agent) getOrCreateProfile(serviceID, participantID, serial, deviceName string) *profile {
	key := profileKey(serviceID, participantID, serial)
	if val, ok := a.profiles.Load(key); ok {
		return val.(*profile)
	}
	p := &profile{
		serviceID:     serviceID,
		participantID: participantID,
		serial:        serial,
		deviceName:    deviceName,
		state:         StateUnregistered,
		notAfter:      make(map[ArtifactID]time.Time),
		issuedAt:      make(map[ArtifactID]time.Time),
		timers:        make(map[ArtifactID]*time.Timer),
	}
	a.profiles.Store(key, p)
	return p
}

// removeInboxFile deletes a fully-processed inbox request file. The outcome
// (success or the failure reason) has already been emitted to the agent log,
// so the request payload itself is no longer needed on disk.
func (a *Agent) removeInboxFile(path string) {
	if err := a.RemoveFile(path); err != nil {
		a.emitf(catWarning, tui.LogWarn, "Warning: inbox could not remove file %s: %v", path, err)
	}
}

// Reset removes all enrolled agent state (the inbox and each provisioning
// service's connext/mTLS artifacts) so the agent starts fresh on its next
// run. It only ever touches .connext/agent — the agent log file and the
// parent .connext directory (which may be shared with the gateway or spy)
// are left in place.
func (a *Agent) Reset() error {
	agentDir := a.Store.AgentDir()
	entries, err := os.ReadDir(agentDir)
	if err != nil {
		if os.IsNotExist(err) {
			_, _ = fmt.Fprintln(a.Out, "No agent state found.")
			return nil
		}
		return err
	}
	logName := filepath.Base(a.Store.LogPath())
	removed := false
	for _, entry := range entries {
		if entry.Name() == logName {
			continue
		}
		path := filepath.Join(agentDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(a.Out, "Removed %s\n", path)
		removed = true
	}
	if !removed {
		_, _ = fmt.Fprintln(a.Out, "No agent state found.")
		return nil
	}
	_, _ = fmt.Fprintln(a.Out)
	_, _ = fmt.Fprintf(a.Out, "Logs were left in\nPath: %s\n", a.Store.LogPath())
	return nil
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
