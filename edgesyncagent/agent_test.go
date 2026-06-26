// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
)

// ─── Helpers ─────────────────────────────────────────────────────────────────

// fakeFS is a minimal in-memory filesystem for tests.
type fakeFS struct {
	mu    sync.RWMutex
	files map[string][]byte
	dirs  map[string]bool
	// removed tracks paths passed to RemoveFile.
	removed []string
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files: map[string][]byte{},
		dirs:  map[string]bool{},
	}
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if data, ok := f.files[path]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
}

func (f *fakeFS) WriteFile(path string, data []byte, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = append([]byte(nil), data...)
	f.addDirsLocked(filepath.Dir(path))
	return nil
}

func (f *fakeFS) MkdirAll(path string, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addDirsLocked(path)
	return nil
}

// addDirsLocked registers dir and all of its ancestors so recursive ReadDir
// walks can descend into them. Caller must hold f.mu.
func (f *fakeFS) addDirsLocked(dir string) {
	for d := dir; d != "" && d != "." && d != string(os.PathSeparator); d = filepath.Dir(d) {
		f.dirs[d] = true
	}
}

func (f *fakeFS) ReadDir(path string) ([]fs.DirEntry, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	seen := map[string]bool{}
	var entries []fs.DirEntry
	for name := range f.files {
		rel, err := filepath.Rel(path, name)
		if err != nil || strings.Contains(rel, string(os.PathSeparator)) || rel == "." {
			continue
		}
		if !seen[rel] {
			seen[rel] = true
			entries = append(entries, fakeDirEntry{name: rel, isDir: false})
		}
	}
	for dir := range f.dirs {
		rel, err := filepath.Rel(path, dir)
		if err != nil || strings.Contains(rel, string(os.PathSeparator)) || rel == "." || rel == ".." {
			continue
		}
		if !seen[rel] {
			seen[rel] = true
			entries = append(entries, fakeDirEntry{name: rel, isDir: true})
		}
	}
	return entries, nil
}

type fakeDirEntry struct {
	name  string
	isDir bool
}

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return e.isDir }
func (e fakeDirEntry) Type() fs.FileMode          { return 0 }
func (e fakeDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

// fakeTimer records scheduled timers and exposes a way to fire them manually.
type fakeTimer struct {
	mu     sync.Mutex
	timers []fakeTimerEntry
	stopCh chan struct{}
	fired  int
}

type fakeTimerEntry struct {
	delay time.Duration
	f     func()
	timer *time.Timer
}

// newFakeAfterFunc returns an AfterFunc replacement and a controller.
func newFakeAfterFunc() (*fakeTimer, func(d time.Duration, f func()) *time.Timer) {
	ft := &fakeTimer{stopCh: make(chan struct{})}
	fn := func(d time.Duration, f func()) *time.Timer {
		// Use a real timer but record it so tests can introspect delays.
		t := time.AfterFunc(d, func() {
			ft.mu.Lock()
			ft.fired++
			ft.mu.Unlock()
			f()
		})
		ft.mu.Lock()
		ft.timers = append(ft.timers, fakeTimerEntry{delay: d, f: f, timer: t})
		ft.mu.Unlock()
		return t
	}
	return ft, fn
}

// buildTestAgent constructs an Agent wired with a fakeFS and no-op Ops.
func buildTestAgent(t *testing.T, ffs *fakeFS) *Agent {
	t.Helper()
	store := &edgestore.Store{
		BaseDir:   "/connext",
		WriteFile: ffs.WriteFile,
		MkdirAll:  ffs.MkdirAll,
		Stat: func(p string) (os.FileInfo, error) {
			ffs.mu.RLock()
			defer ffs.mu.RUnlock()
			if _, ok := ffs.files[p]; ok {
				return fakeFileInfo{}, nil
			}
			return nil, os.ErrNotExist
		},
	}
	var logBuf strings.Builder
	logger := io.Writer(&logBuf)

	a := NewAgent(store, logger)
	a.EnrollFunc = func(serviceID, participantID, serial string, _ []string, _, keyFile, _ string) (string, error) {
		keyData, _ := os.ReadFile(keyFile)
		// Simulate EnrollDevice returning a domain template and writing the node
		// key into the layered mTLS slot at that domain.
		mtlsDir := a.Store.NodeAgentDir(serviceID, "dom", participantID, serial)
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(a.Store.NodeKeyPath(serviceID, "dom", participantID, serial), keyData, 0o600)
		return "dom", nil
	}
	a.RequestIdentityFunc = func(_, _, _, _, _, _, output string) error {
		// Write a fake identity cert so renewIdentity can copy it to node.crt.
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "identity.crt"), []byte("FAKE-CERT"), 0o644)
		return nil
	}
	a.RequestPermissionsFunc = func(_, _, _, _, _, _ string) error { return nil }
	a.RequestPSKFunc = func(_, _, _, _, _, _ string) error { return nil }
	a.GetCRLFunc = func(_, _, _, _, _, _ string) error { return nil }
	a.RenewDeviceCertFunc = func(_, _, _, _, _, _ string, _ int, _ string) error { return nil }
	a.GenerateKeyAndCSRFunc = func(cn, org, tmpDir string) (string, string, error) {
		keyPath := filepath.Join(tmpDir, "key.pem")
		csrPath := filepath.Join(tmpDir, "csr.pem")
		_ = os.WriteFile(keyPath, []byte("KEY"), 0o600)
		_ = os.WriteFile(csrPath, []byte("CSR"), 0o644)
		return keyPath, csrPath, nil
	}
	a.GenerateCSRFromKeyFunc = func(cn, org string, keyPEM []byte, tmpDir string) (string, error) {
		csrPath := filepath.Join(tmpDir, "csr.pem")
		_ = os.WriteFile(csrPath, []byte("CSR"), 0o644)
		return csrPath, nil
	}
	// ReadFile falls back to the real OS for paths outside the fake store
	// (e.g. temp key/CSR files written by GenerateKeyAndCSR).
	a.ReadFile = func(path string) ([]byte, error) {
		if data, err := ffs.ReadFile(path); err == nil {
			return data, nil
		}
		return os.ReadFile(path)
	}
	a.WriteFile = ffs.WriteFile
	a.MkdirAll = ffs.MkdirAll
	a.ReadDir = ffs.ReadDir
	a.RemoveFile = func(path string) error {
		ffs.mu.Lock()
		defer ffs.mu.Unlock()
		delete(ffs.files, path)
		ffs.removed = append(ffs.removed, path)
		return nil
	}
	a.InboxDir = "/connext/inbox"
	a.PollInterval = 50 * time.Millisecond
	a.SweepInterval = 50 * time.Millisecond
	return a
}

// leaseJSON builds a minimal *_lease.json payload.
func leaseJSON(notBefore, notAfter time.Time) []byte {
	type leasePayload struct {
		Lease struct {
			NotBefore time.Time `json:"notBefore"`
			NotAfter  time.Time `json:"notAfter"`
		} `json:"lease"`
	}
	var p leasePayload
	p.Lease.NotBefore = notBefore
	p.Lease.NotAfter = notAfter
	data, _ := json.Marshal(p)
	return data
}

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

// ─── RenewalDelay ─────────────────────────────────────────────────────────────

func TestRenewalDelay_AtStart(t *testing.T) {
	now := time.Unix(0, 0)
	notBefore := now
	notAfter := now.Add(100 * time.Second)
	// At 0% of lifetime the threshold (80%) is 80s away.
	got := RenewalDelay(now, notBefore, notAfter)
	want := 80 * time.Second
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRenewalDelay_AtHalfway(t *testing.T) {
	now := time.Unix(0, 0)
	notBefore := now
	notAfter := now.Add(100 * time.Second)
	// At 50% of lifetime the threshold is still 30s away.
	got := RenewalDelay(now.Add(50*time.Second), notBefore, notAfter)
	want := 30 * time.Second
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRenewalDelay_PastThreshold(t *testing.T) {
	now := time.Unix(0, 0)
	notBefore := now
	notAfter := now.Add(100 * time.Second)
	// At 90% of lifetime the threshold has already passed.
	got := RenewalDelay(now.Add(90*time.Second), notBefore, notAfter)
	if got != 0 {
		t.Fatalf("expected 0, got %v", got)
	}
}

func TestRenewalDelay_ZeroNotAfter(t *testing.T) {
	if d := RenewalDelay(time.Now(), time.Now(), time.Time{}); d != 0 {
		t.Fatalf("expected 0 for zero notAfter, got %v", d)
	}
}

func TestRenewalDelay_AlreadyExpired(t *testing.T) {
	now := time.Now()
	if d := RenewalDelay(now, now.Add(-200*time.Second), now.Add(-1*time.Second)); d != 0 {
		t.Fatalf("expected 0 for expired artifact, got %v", d)
	}
}

// ─── Startup rehydration ─────────────────────────────────────────────────────

func TestRehydrate_LoadsProfileAndSchedulesTimers(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	timerCount := 0
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		timerCount++
		// Return a timer that never fires during the test.
		return time.AfterFunc(10*time.Hour, f)
	}
	a.Now = func() time.Time { return time.Unix(0, 0) }

	// Populate an agent_state.json for one profile.
	now := time.Unix(0, 0)
	notAfter := now.Add(100 * time.Second)
	st := AgentState{
		State:                 StateActive,
		Serial:                "SN-001",
		ServiceID:             "svc1",
		DomainTemplateID:      "dom1",
		ParticipantTemplateID: "part1",
		DeviceName:            "dev1",
		NotAfter: map[ArtifactID]time.Time{
			ArtifactIdentity:    notAfter,
			ArtifactPermissions: notAfter,
		},
		IssuedAt: map[ArtifactID]time.Time{
			ArtifactIdentity:    now,
			ArtifactPermissions: now,
		},
	}
	data, _ := json.Marshal(st)
	ffs.WriteFile(a.Store.NodeStatePath("svc1", "dom1", "part1", "SN-001"), data, 0o644)

	a.rehydrate()

	val, ok := a.profiles.Load(profileKey("dom1", "part1", "dev1"))
	if !ok {
		t.Fatal("profile not loaded")
	}
	p := val.(*profile)
	if p.state != StateActive {
		t.Fatalf("expected StateActive, got %q", p.state)
	}
	if timerCount != 2 {
		t.Fatalf("expected 2 timers scheduled, got %d", timerCount)
	}
}

func TestRehydrate_SkipsCorruptStateFile(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	ffs.WriteFile(a.Store.NodeStatePath("svc1", "svc1", "part1", "SN-001"), []byte("not json"), 0o644)

	a.rehydrate() // must not panic

	_, ok := a.profiles.Load(profileKey("svc1", "part1", "dev1"))
	if ok {
		t.Fatal("corrupt profile should not be loaded")
	}
}

// ─── Timer premature-fire rescheduling ────────────────────────────────────────

func TestOnTimerFire_PrematurityReschedules(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	rescheduled := false
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		rescheduled = true
		return time.AfterFunc(10*time.Hour, f)
	}

	now := time.Unix(0, 0)
	notAfter := now.Add(100 * time.Second)
	a.Now = func() time.Time { return now } // clock hasn't advanced — timer fired prematurely

	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	p.notAfter[ArtifactIdentity] = notAfter
	p.mu.Unlock()

	// Fire the timer before the renewal threshold (80s into a 100s window).
	a.onTimerFire(p, ArtifactIdentity, notAfter)

	if !rescheduled {
		t.Fatal("expected timer to be rescheduled on premature fire")
	}
}

func TestOnTimerFire_IgnoresStaleTimer(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	renewCalled := false
	a.RequestIdentityFunc = func(_, _, _, _, _, _, _ string) error {
		renewCalled = true
		return nil
	}

	now := time.Unix(0, 0)
	oldNotAfter := now.Add(100 * time.Second)
	newNotAfter := now.Add(200 * time.Second)
	a.Now = func() time.Time { return now.Add(90 * time.Second) } // past threshold

	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	// Profile has a newer notAfter than what the timer was scheduled for.
	p.notAfter[ArtifactIdentity] = newNotAfter
	p.mu.Unlock()

	a.onTimerFire(p, ArtifactIdentity, oldNotAfter)

	if renewCalled {
		t.Fatal("stale timer should not trigger renewal")
	}
}

// ─── Sanity sweep ─────────────────────────────────────────────────────────────

func TestSweep_TriggersRenewalForExpiredArtifact(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	renewed := make(chan ArtifactID, 1)
	a.RequestPermissionsFunc = func(_, _, _, _, _, _ string) error {
		renewed <- ArtifactPermissions
		return nil
	}

	now := time.Unix(1000, 0)
	a.Now = func() time.Time { return now }
	// AfterFunc that never fires — we're testing the sweep path.
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	p.domainTemplateID = "dom"
	p.serial = "SN-001"
	// notAfter in the past — threshold already passed.
	p.notAfter[ArtifactPermissions] = now.Add(-10 * time.Second)
	p.mu.Unlock()
	// Write device URL so ResolveNodeURL succeeds.
	ffs.WriteFile(a.Store.NodeURLPath("svc", "dom", "part", "SN-001"), []byte("https://svc.devices.example.com"), 0o644)

	a.wg.Add(0)
	a.sweep()

	select {
	case got := <-renewed:
		if got != ArtifactPermissions {
			t.Fatalf("wrong artifact renewed: %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sweep did not trigger renewal within timeout")
	}
}

// ─── Inbox processing ────────────────────────────────────────────────────────

func TestDrainInbox_ProcessesValidRequest(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	enrolled := make(chan struct{}, 1)
	a.EnrollFunc = func(serviceID, participantID, serial string, _ []string, _, keyFile, _ string) (string, error) {
		keyData, _ := os.ReadFile(keyFile)
		// Simulate EnrollDevice returning a domain template and writing the node
		// key into the layered mTLS slot at that domain.
		mtlsDir := a.Store.NodeAgentDir(serviceID, "dom", participantID, serial)
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "node.key"), keyData, 0o600)
		enrolled <- struct{}{}
		return "dom", nil
	}
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	req := EnrollRequest{
		ServiceID:     "svc1",
		ParticipantID: "part1",
		CampaignToken: buildJWT(map[string]any{"device_domain": "svc1.devices.cloud.dev-rti.com"}),
		Serial:        "SN-001",
		MACs:          []string{"AA:BB:CC:DD:EE:01"},
	}
	data, _ := json.Marshal(req)
	inboxFile := "/connext/inbox/enroll-test.json"
	ffs.WriteFile(inboxFile, data, 0o644)

	a.drainInbox()

	select {
	case <-enrolled:
	case <-time.After(2 * time.Second):
		t.Fatal("enroll not called within timeout")
	}

	// File should have been removed from the inbox after a successful enroll.
	if !contains(ffs.removed, inboxFile) {
		t.Fatalf("inbox file not removed after success; removed: %v", ffs.removed)
	}
}

// contains reports whether s is present in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestDrainInbox_RejectsInvalidJSON(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	inboxFile := "/connext/inbox/enroll-bad.json"
	ffs.WriteFile(inboxFile, []byte("{not json"), 0o644)

	a.drainInbox()

	if !contains(ffs.removed, inboxFile) {
		t.Fatalf("bad file not removed from inbox; removed: %v", ffs.removed)
	}
}

func TestDrainInbox_RejectsMissingFields(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	req := EnrollRequest{ServiceID: "svc"} // participant_id, serial, macs missing
	data, _ := json.Marshal(req)
	inboxFile := "/connext/inbox/enroll-incomplete.json"
	ffs.WriteFile(inboxFile, data, 0o644)

	a.drainInbox()

	if !contains(ffs.removed, inboxFile) {
		t.Fatalf("incomplete request not removed from inbox; removed: %v", ffs.removed)
	}
}

func TestDrainInbox_SkipsTmpFiles(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	enrollCalled := false
	a.EnrollFunc = func(_, _, _ string, _ []string, _, _, _ string) (string, error) {
		enrollCalled = true
		return "", nil
	}

	// .tmp file should be ignored by drainInbox.
	ffs.WriteFile("/connext/inbox/enroll-pending.json.tmp", []byte("{}"), 0o644)

	a.drainInbox()

	if enrollCalled {
		t.Fatal(".tmp file must not be processed")
	}
}

func TestDrainInbox_EnrollmentFailureMovesToFailed(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	a.EnrollFunc = func(_, _, _ string, _ []string, _, _, _ string) (string, error) {
		return "", os.ErrPermission
	}
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	req := EnrollRequest{
		ServiceID: "svc", ParticipantID: "part", Serial: "SN-001",
		MACs: []string{"AA:BB:CC:DD:EE:01"},
	}
	data, _ := json.Marshal(req)
	inboxFile := "/connext/inbox/enroll-fail.json"
	ffs.WriteFile(inboxFile, data, 0o644)

	a.drainInbox()

	// processInboxFile runs enrollProfile in the same goroutine, so by the time
	// drainInbox returns the file has been removed.
	if !contains(ffs.removed, inboxFile) {
		t.Fatalf("failed enrollment not removed from inbox; removed: %v", ffs.removed)
	}
}

// ─── State persistence ────────────────────────────────────────────────────────

func TestPersistState_WritesAgentStateJSON(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	now := time.Now().Truncate(time.Second)
	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	p.serial = "SN-001"
	p.domainTemplateID = "dom"
	p.state = StateActive
	p.notAfter[ArtifactIdentity] = now.Add(100 * time.Second)
	p.notAfter[ArtifactPermissions] = now.Add(200 * time.Second)
	p.mu.Unlock()

	if err := a.persistState(p); err != nil {
		t.Fatalf("persistState: %v", err)
	}

	data, err := ffs.ReadFile(a.Store.NodeStatePath("svc", "dom", "part", "SN-001"))
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}

	var st AgentState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.State != StateActive {
		t.Errorf("state: got %q want %q", st.State, StateActive)
	}
	if st.NotAfter[ArtifactIdentity].IsZero() {
		t.Error("identity notAfter not persisted")
	}
	if st.NotAfter[ArtifactPermissions].IsZero() {
		t.Error("permissions notAfter not persisted")
	}
}

// ─── readLeaseNotAfter ────────────────────────────────────────────────────────

func TestReadLeaseNotAfter_ParsesNotAfter(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	notAfter := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	ffs.WriteFile("/connext/svc/part/connext_artifacts/identity_lease.json",
		leaseJSON(notAfter.Add(-24*time.Hour), notAfter), 0o644)

	got := a.readLeaseNotAfter("/connext/svc/part/connext_artifacts/identity_lease.json")
	if !got.Equal(notAfter) {
		t.Fatalf("got %v want %v", got, notAfter)
	}
}

func TestReadLeaseNotAfter_ReturnsZeroOnMissingFile(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	got := a.readLeaseNotAfter("/connext/missing.json")
	if !got.IsZero() {
		t.Fatalf("expected zero time, got %v", got)
	}
}

// ─── enrollProfile state transitions ─────────────────────────────────────────

func TestEnrollProfile_StateTransitionsToActive(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}
	a.Now = func() time.Time { return time.Unix(0, 0) }

	notAfter := time.Unix(0, 0).Add(100 * time.Second)
	a.EnrollFunc = func(serviceID, participantID, serial string, _ []string, _, keyFile, _ string) (string, error) {
		// Simulate EnrollDevice writing the device key to the mTLS store.
		keyData, _ := os.ReadFile(keyFile)
		// Simulate EnrollDevice returning a domain template and writing the node
		// key into the layered mTLS slot at that domain.
		mtlsDir := a.Store.NodeAgentDir(serviceID, "dom", participantID, serial)
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "node.key"), keyData, 0o600)
		ffs.WriteFile(filepath.Join(mtlsDir, "node.crt"), []byte("FAKE-CERT"), 0o644)
		ffs.WriteFile(filepath.Join(mtlsDir, "ca-chain.crt"), []byte("FAKE-CA"), 0o644)
		return "dom", nil
	}
	a.RequestIdentityFunc = func(_, _, _, _, _, _, output string) error {
		// Write a lease file and a fake identity cert to the output directory.
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		leasePath := filepath.Join(dir, "identity_lease.json")
		ffs.WriteFile(leasePath, leaseJSON(time.Unix(0, 0), notAfter), 0o644)
		ffs.WriteFile(filepath.Join(dir, "identity.crt"), []byte("FAKE-CERT"), 0o644)
		return nil
	}

	req := EnrollRequest{
		ServiceID:     "svc",
		ParticipantID: "part",
		CampaignToken: buildJWT(map[string]any{"device_domain": "svc.devices.cloud.dev-rti.com"}),
		Serial:        "SN-001",
		MACs:          []string{"AA:BB:CC:DD:EE:01"},
		DeviceName:    "dev",
	}
	if err := a.enrollProfile(req); err != nil {
		t.Fatalf("enrollProfile: %v", err)
	}

	// After a successful enroll the profile is re-keyed under its domain.
	val, ok := a.profiles.Load(profileKey("dom", "part", "dev"))
	if !ok {
		t.Fatal("profile not stored")
	}
	p := val.(*profile)
	p.mu.Lock()
	st := p.state
	p.mu.Unlock()
	if st != StateActive {
		t.Fatalf("expected StateActive after enroll, got %q", st)
	}
}

func TestEnrollProfile_DomainArtifactsDedupedAcrossParticipants(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}
	a.Now = func() time.Time { return time.Unix(0, 0) }

	// Lease far in the future so no immediate background renewal fires.
	pskNotAfter := time.Unix(0, 0).Add(1000 * time.Second)

	var pskCalls, crlCalls int32
	a.RequestPSKFunc = func(_, _, _, _, _, output string) error {
		atomic.AddInt32(&pskCalls, 1)
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "psk_primary.txt"), []byte("sA"), 0o644)
		ffs.WriteFile(filepath.Join(dir, "psk_extra.txt"), []byte("sA\nsB"), 0o644)
		ffs.WriteFile(filepath.Join(dir, "psk_lease.json"), pskLeaseJSON(pskNotAfter, pskNotAfter.Add(100*time.Second)), 0o644)
		return nil
	}
	a.GetCRLFunc = func(_, _, _, _, _, _ string) error { atomic.AddInt32(&crlCalls, 1); return nil }
	a.RequestIdentityFunc = func(_, _, _, _, _, _, output string) error {
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "identity.crt"), []byte("CERT"), 0o644)
		return nil
	}

	token := buildJWT(map[string]any{"device_domain": "svc.devices.cloud.dev-rti.com"})
	mkReq := func(part string) EnrollRequest {
		return EnrollRequest{ServiceID: "svc", ParticipantID: part, CampaignToken: token, Serial: "SN-001", MACs: []string{"AA:BB:CC:DD:EE:01"}}
	}
	// Two participants in the SAME (service, domain) — the mock returns "dom".
	if err := a.enrollProfile(mkReq("part1")); err != nil {
		t.Fatalf("enroll part1: %v", err)
	}
	if err := a.enrollProfile(mkReq("part2")); err != nil {
		t.Fatalf("enroll part2: %v", err)
	}

	if got := atomic.LoadInt32(&pskCalls); got != 1 {
		t.Fatalf("PSK fetched %d times, want 1 (domain-scoped dedup)", got)
	}
	if got := atomic.LoadInt32(&crlCalls); got != 1 {
		t.Fatalf("CRL fetched %d times, want 1 (domain-scoped dedup)", got)
	}

	v1, _ := a.profiles.Load(profileKey("dom", "part1", ""))
	v2, _ := a.profiles.Load(profileKey("dom", "part2", ""))
	if v1 == nil || v2 == nil {
		t.Fatal("both participant profiles should be stored")
	}
	if !a.isDomainOwner(v1.(*profile)) {
		t.Fatal("part1 (first to enroll) should own the domain")
	}
	if a.isDomainOwner(v2.(*profile)) {
		t.Fatal("part2 should not own the domain")
	}
	// The non-owner must not schedule domain-scoped timers.
	p2 := v2.(*profile)
	p2.mu.Lock()
	_, hasPSK := p2.timers[ArtifactPSK]
	_, hasCRL := p2.timers[ArtifactCRL]
	p2.mu.Unlock()
	if hasPSK || hasCRL {
		t.Fatal("non-owner participant must not schedule PSK/CRL timers")
	}
}

func TestClaimDomainOwner_PrefersProfileWithDomainState(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	mk := func(part string, withPSK bool) *profile {
		p := &profile{
			serviceID: "svc", domainTemplateID: "dom", participantID: part, serial: "SN-001",
			notAfter: map[ArtifactID]time.Time{}, issuedAt: map[ArtifactID]time.Time{},
			timers: map[ArtifactID]*time.Timer{},
		}
		if withPSK {
			p.notAfter[ArtifactPSK] = time.Unix(100, 0)
		}
		return p
	}
	owner := mk("part1", true) // the original fetcher carries the domain state
	other := mk("part2", false)

	// Claim in the "wrong" order (stateless first), as rehydrate might.
	a.claimDomainOwner(other)
	a.claimDomainOwner(owner)

	if !a.isDomainOwner(owner) {
		t.Fatal("the profile holding domain state should win ownership regardless of claim order")
	}
	if a.isDomainOwner(other) {
		t.Fatal("the stateless profile should yield ownership")
	}
}

func TestFindNodeDomain_ResolvesStoredNode(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	// Simulate a node enrolled under domain "dom".
	ffs.WriteFile(a.Store.NodeKeyPath("svc", "dom", "part", "SN-001"), []byte("KEY"), 0o600)

	if got := a.findNodeDomain("svc", "part", "SN-001"); got != "dom" {
		t.Fatalf("findNodeDomain = %q, want dom", got)
	}
	if got := a.findNodeDomain("svc", "part", "SN-999"); got != "" {
		t.Fatalf("findNodeDomain for unknown node = %q, want empty", got)
	}
}

func TestEnrollProfile_EnrollErrorLeavesUnregistered(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.EnrollFunc = func(_, _, _ string, _ []string, _, _, _ string) (string, error) {
		return "", os.ErrPermission
	}
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	req := EnrollRequest{
		ServiceID: "svc", ParticipantID: "part", Serial: "SN-001",
		MACs: []string{"AA:BB:CC:DD:EE:01"}, DeviceName: "dev",
	}
	err := a.enrollProfile(req)
	if err == nil {
		t.Fatal("expected error from failed enroll")
	}

	val, ok := a.profiles.Load(profileKey("svc", "part", "dev"))
	if !ok {
		t.Fatal("profile not stored after failed enroll")
	}
	p := val.(*profile)
	p.mu.Lock()
	st := p.state
	p.mu.Unlock()
	if st != StateUnregistered {
		t.Fatalf("expected StateUnregistered after enroll failure, got %q", st)
	}
}

// ─── Run (integration smoke-test) ─────────────────────────────────────────────

func TestRun_StartsAndStopsCleanly(t *testing.T) {
	ffs := newFakeFS()
	// Seed a profile so rehydrate finds it and the first-run wizard is skipped.
	stateData, _ := json.Marshal(AgentState{State: StateActive, NotAfter: map[ArtifactID]time.Time{}, IssuedAt: map[ArtifactID]time.Time{}, ServiceID: "svc", ParticipantTemplateID: "part", Serial: "SN-001", DeviceName: "dev"})
	a := buildTestAgent(t, ffs)
	_ = ffs.WriteFile(a.Store.NodeStatePath("svc", "svc", "part", "SN-001"), stateData, 0o644)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancel")
	}
}

func TestRun_ProcessesInboxFileDuringOperation(t *testing.T) {
	ffs := newFakeFS()
	// Seed a profile so rehydrate finds it and the first-run wizard is skipped.
	stateData, _ := json.Marshal(AgentState{State: StateActive, NotAfter: map[ArtifactID]time.Time{}, IssuedAt: map[ArtifactID]time.Time{}, ServiceID: "svc", ParticipantTemplateID: "part", Serial: "SN-001", DeviceName: "dev"})
	a := buildTestAgent(t, ffs)
	_ = ffs.WriteFile(a.Store.NodeStatePath("svc", "svc", "part", "SN-001"), stateData, 0o644)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	enrolled := make(chan struct{}, 1)
	a.EnrollFunc = func(serviceID, participantID, serial string, _ []string, _, keyFile, _ string) (string, error) {
		keyData, _ := os.ReadFile(keyFile)
		// Simulate EnrollDevice returning a domain template and writing the node
		// key into the layered mTLS slot at that domain.
		mtlsDir := a.Store.NodeAgentDir(serviceID, "dom", participantID, serial)
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "node.key"), keyData, 0o600)
		select {
		case enrolled <- struct{}{}:
		default:
		}
		return "dom", nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = a.Run(ctx) }()

	// Give the Run loop a moment to start, then drop an inbox file.
	time.Sleep(20 * time.Millisecond)
	req := EnrollRequest{
		ServiceID:     "svc",
		ParticipantID: "part",
		CampaignToken: buildJWT(map[string]any{"device_domain": "svc.devices.cloud.dev-rti.com"}),
		Serial:        "SN-001",
		MACs:          []string{"AA:BB:CC:DD:EE:01"},
	}
	data, _ := json.Marshal(req)
	ffs.WriteFile("/connext/inbox/enroll-live.json", data, 0o644)

	select {
	case <-enrolled:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not process inbox file during operation")
	}
	cancel()
}

// ─── getOrCreateProfile ───────────────────────────────────────────────────────

func TestGetOrCreateProfile_ReturnsSameInstance(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	p1 := a.getOrCreateProfile("svc", "part", "dev")
	p2 := a.getOrCreateProfile("svc", "part", "dev")
	if p1 != p2 {
		t.Fatal("expected the same profile instance")
	}
}

func TestGetOrCreateProfile_DifferentKeysDistinct(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	p1 := a.getOrCreateProfile("svc", "part1", "dev")
	p2 := a.getOrCreateProfile("svc", "part2", "dev")
	if p1 == p2 {
		t.Fatal("distinct participant IDs must produce distinct profiles")
	}
}

func TestGetOrCreateProfile_DifferentDeviceNamesDistinct(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	p1 := a.getOrCreateProfile("svc", "part", "dev1")
	p2 := a.getOrCreateProfile("svc", "part", "dev2")
	if p1 == p2 {
		t.Fatal("distinct device names must produce distinct profiles")
	}
}

// ─── removeInboxFile ──────────────────────────────────────────────────────────

func TestRemoveInboxFile_DeletesFile(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	src := "/connext/inbox/enroll-x.json"
	ffs.WriteFile(src, []byte("{}"), 0o644)

	a.removeInboxFile(src)

	if _, err := ffs.ReadFile(src); err == nil {
		t.Fatal("inbox file should have been removed")
	}
	if !contains(ffs.removed, src) {
		t.Fatalf("removeInboxFile did not call RemoveFile; removed: %v", ffs.removed)
	}
}

// ─── Device cert renewal ──────────────────────────────────────────────────────

func TestRenewArtifact_DeviceCert_Success(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	// Fake PEM key + URL in the layered mTLS slot (node = serial).
	ffs.WriteFile(a.Store.NodeKeyPath("svc", "dom", "part", "SN-001"), []byte("KEY"), 0o600)
	ffs.WriteFile(a.Store.NodeURLPath("svc", "dom", "part", "SN-001"), []byte("https://svc.devices.example.com"), 0o644)

	// Build a minimal self-signed cert to act as the renewed device cert.
	notAfterWant := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	certPEM := buildFakeCertPEM(t, notAfterWant)

	renewCalled := make(chan struct{}, 1)
	a.RenewDeviceCertFunc = func(_, _, _, _, _, csrFile string, _ int, output string) error {
		// Write the fake renewed cert where the agent expects it.
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "node.crt"), certPEM, 0o644)
		ffs.WriteFile(filepath.Join(dir, "ca-chain.crt"), []byte("CA"), 0o644)
		renewCalled <- struct{}{}
		return nil
	}
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	now := time.Unix(1000, 0)
	a.Now = func() time.Time { return now }

	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	p.serial = "SN-001"
	p.domainTemplateID = "dom"
	// Expire the device-cert notAfter so the timer fires immediately.
	p.notAfter[ArtifactDeviceCert] = now.Add(-1 * time.Second)
	p.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.renewArtifact(p, ArtifactDeviceCert, "test")
	}()

	select {
	case <-renewCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("RenewDeviceCertFunc not called within timeout")
	}
	a.wg.Wait()

	p.mu.Lock()
	notAfterGot := p.notAfter[ArtifactDeviceCert]
	p.mu.Unlock()

	if notAfterGot.IsZero() {
		t.Fatal("device-cert notAfter should be updated after renewal")
	}
}

func TestRenewArtifact_DeviceCert_IsInAllArtifacts(t *testing.T) {
	found := false
	for _, id := range allArtifacts {
		if id == ArtifactDeviceCert {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("ArtifactDeviceCert must be in allArtifacts so timers are scheduled")
	}
}

// buildFakeCertPEM creates a minimal self-signed DER certificate with the
// given NotAfter encoded as a PEM block.  It is only intended for reading
// the NotAfter field — it is not a real certificate.
func buildFakeCertPEM(t *testing.T, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    notAfter.Add(-1 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ─── PSK rolling-key timers ───────────────────────────────────────────────────

// pskLeaseJSON builds a psk_lease.json with separate pskA and pskB slots, as
// returned by POST /psk and parsed by readPSKABNotAfter.
func pskLeaseJSON(pskANotAfter, pskBNotAfter time.Time) []byte {
	slot := func(notAfter time.Time) map[string]any {
		return map[string]any{"lease": map[string]any{"notAfter": notAfter}}
	}
	data, _ := json.Marshal(map[string]any{
		"pskA": slot(pskANotAfter),
		"pskB": slot(pskBNotAfter),
	})
	return data
}

// TestRenewPSKAt80_RotateFiresAtPrimaryExpiry verifies that the 80% PSK renewal
// arms the rotation for the CURRENT primary's expiry (sA), not the next seed's
// expiry (sB).  Arming it for sB would skip sA's rotation and delay the first
// rotation by an entire key period (the "first interval is 2x the rest" bug).
func TestRenewPSKAt80_RotateFiresAtPrimaryExpiry(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	now := time.Unix(1000, 0)
	a.Now = func() time.Time { return now }
	// Keep timers from firing during the test.
	a.AfterFunc = func(_ time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	// sA (current primary) expires before sB (the staged next seed) — that
	// stagger is the whole point of the overlap window.
	pskANotAfter := now.Add(100 * time.Second)
	pskBNotAfter := now.Add(200 * time.Second)

	// PSK is domain-scoped.
	outDir := a.Store.DomainDir("svc", "dom")
	// renewPSKAt80 reads psk_primary.txt before the server call.
	ffs.WriteFile(filepath.Join(outDir, "psk_primary.txt"), []byte("sA"), 0o644)

	a.RequestPSKFunc = func(_, _, _, _, _, output string) error {
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "psk_primary.txt"), []byte("sA"), 0o644)
		ffs.WriteFile(filepath.Join(dir, "psk_extra.txt"), []byte("sA\nsB"), 0o644)
		ffs.WriteFile(filepath.Join(dir, "psk_lease.json"), pskLeaseJSON(pskANotAfter, pskBNotAfter), 0o644)
		return nil
	}

	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	p.domainTemplateID = "dom"
	p.notAfter[ArtifactPSK] = now // old expiry, so newNotAfter advances
	p.issuedAt[ArtifactPSK] = now
	p.pskBaseTTL = 100 * time.Second // baseTTL of sA
	p.pskBNotAfter = pskBNotAfter    // current sB expiry (must NOT leak into rotate)
	p.notAfter[ArtifactPSKRotate] = pskANotAfter
	p.mu.Unlock()

	a.renewArtifact(p, ArtifactPSK, "test")

	p.mu.Lock()
	gotRotate := p.notAfter[ArtifactPSKRotate]
	gotCleanup := p.notAfter[ArtifactPSKCleanup]
	p.mu.Unlock()

	if !gotRotate.Equal(pskANotAfter) {
		t.Errorf("ArtifactPSKRotate = %v, want sA's expiry %v (got sB's expiry %v means the first rotation is delayed a full period)",
			gotRotate, pskANotAfter, pskBNotAfter)
	}
	wantCleanup := pskANotAfter.Add(100 * time.Second / 5)
	if !gotCleanup.Equal(wantCleanup) {
		t.Errorf("ArtifactPSKCleanup = %v, want %v (120%% of sA)", gotCleanup, wantCleanup)
	}
}

// pskLeaseJSONFull builds a psk_lease.json with both not_before and not_after
// for each slot, so tests can exercise the lease-anchored issuedAt path.
func pskLeaseJSONFull(aNotBefore, aNotAfter, bNotBefore, bNotAfter time.Time) []byte {
	slot := func(nb, na time.Time) map[string]any {
		return map[string]any{"lease": map[string]any{"notBefore": nb, "notAfter": na}}
	}
	data, _ := json.Marshal(map[string]any{
		"pskA": slot(aNotBefore, aNotAfter),
		"pskB": slot(bNotBefore, bNotAfter),
	})
	return data
}

// TestRenewPSK_IssuedAtAnchoredToLeaseNotBefore verifies the 80% renewal anchors
// issuedAt[psk] to sA's real lease not_before (not "now"), so the 80% point is
// stable across renewals instead of drifting toward the expiry.
func TestRenewPSK_IssuedAtAnchoredToLeaseNotBefore(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	now := time.Unix(10000, 0)
	a.Now = func() time.Time { return now }
	a.AfterFunc = func(_ time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	// sA was minted before this agent picked it up: notBefore is in the past.
	pskANotBefore := now.Add(-30 * time.Second)
	pskANotAfter := now.Add(70 * time.Second)
	pskBNotBefore := pskANotAfter
	pskBNotAfter := pskANotAfter.Add(100 * time.Second)

	// PSK is domain-scoped.
	outDir := a.Store.DomainDir("svc", "dom")
	ffs.WriteFile(filepath.Join(outDir, "psk_primary.txt"), []byte("sA"), 0o644)
	a.RequestPSKFunc = func(_, _, _, _, _, output string) error {
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "psk_primary.txt"), []byte("sA"), 0o644)
		ffs.WriteFile(filepath.Join(dir, "psk_extra.txt"), []byte("sA\nsB"), 0o644)
		ffs.WriteFile(filepath.Join(dir, "psk_lease.json"),
			pskLeaseJSONFull(pskANotBefore, pskANotAfter, pskBNotBefore, pskBNotAfter), 0o644)
		return nil
	}

	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	p.domainTemplateID = "dom"
	p.notAfter[ArtifactPSK] = now // stale, so newNotAfter advances
	p.issuedAt[ArtifactPSK] = now
	p.pskBaseTTL = 100 * time.Second
	p.mu.Unlock()

	a.renewArtifact(p, ArtifactPSK, "test")

	p.mu.Lock()
	gotIssued := p.issuedAt[ArtifactPSK]
	gotBNotBefore := p.pskBNotBefore
	p.mu.Unlock()

	if !gotIssued.Equal(pskANotBefore) {
		t.Errorf("issuedAt[psk] = %v, want sA lease notBefore %v (anchored, not now=%v — drifting issuedAt pushes the 80%% point toward expiry)",
			gotIssued, pskANotBefore, now)
	}
	if !gotBNotBefore.Equal(pskBNotBefore) {
		t.Errorf("pskBNotBefore = %v, want %v (needed by pskRotate to anchor sB's window)", gotBNotBefore, pskBNotBefore)
	}
}

// TestPSKRotate_AdvancesWindowToSB verifies that the 100% rotation advances the
// ENTIRE ArtifactPSK window (notAfter + issuedAt) to sB and arms a fresh 80%
// renewal timer — so the TUI never shows a false "needs renewal" for the freshly
// rotated-in key.
func TestPSKRotate_AdvancesWindowToSB(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	now := time.Unix(50000, 0)
	a.Now = func() time.Time { return now }
	a.AfterFunc = func(_ time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	// PSK is domain-scoped.
	outDir := a.Store.DomainDir("svc", "dom")
	ffs.WriteFile(filepath.Join(outDir, "psk_primary.txt"), []byte("sA"), 0o644)
	ffs.WriteFile(filepath.Join(outDir, "psk_temp.txt"), []byte("sB"), 0o644)

	baseTTL := 100 * time.Second
	pskBNotBefore := now // sB starts exactly as sA expires
	pskBNotAfter := now.Add(baseTTL)

	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	p.domainTemplateID = "dom"
	p.notAfter[ArtifactPSK] = now // stale sA expiry (== now → would render "needs renewal")
	p.issuedAt[ArtifactPSK] = now.Add(-baseTTL)
	p.notAfter[ArtifactPSKRotate] = now
	p.pskBaseTTL = baseTTL
	p.pskBNotAfter = pskBNotAfter
	p.pskBNotBefore = pskBNotBefore
	p.mu.Unlock()

	a.pskRotate(p)

	p.mu.Lock()
	defer p.mu.Unlock()
	if got := p.notAfter[ArtifactPSK]; !got.Equal(pskBNotAfter) {
		t.Errorf("notAfter[psk] = %v, want sB expiry %v (stale sA value causes a false \"needs renewal\")", got, pskBNotAfter)
	}
	if !p.notAfter[ArtifactPSK].After(now) {
		t.Error("notAfter[psk] must be in the future after rotation (no false needs-renewal)")
	}
	if got := p.issuedAt[ArtifactPSK]; !got.Equal(pskBNotBefore) {
		t.Errorf("issuedAt[psk] = %v, want sB notBefore %v", got, pskBNotBefore)
	}
	if got := p.notAfter[ArtifactPSKRotate]; !got.Equal(pskBNotAfter) {
		t.Errorf("rotate = %v, want sB expiry %v", got, pskBNotAfter)
	}
	wantCleanup := pskBNotAfter.Add(baseTTL / 5)
	if got := p.notAfter[ArtifactPSKCleanup]; !got.Equal(wantCleanup) {
		t.Errorf("cleanup = %v, want %v (120%% of sB)", got, wantCleanup)
	}
	if p.timers[ArtifactPSK] == nil {
		t.Error("expected a fresh ArtifactPSK 80% renewal timer armed for sB after rotation")
	}
}
