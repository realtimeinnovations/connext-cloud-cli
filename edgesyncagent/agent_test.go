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
	// renamed tracks (src → dst) pairs that were renamed.
	renamed [][2]string
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
	f.dirs[filepath.Dir(path)] = true
	return nil
}

func (f *fakeFS) MkdirAll(path string, _ os.FileMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs[path] = true
	return nil
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

func (f *fakeFS) Rename(src, dst string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if data, ok := f.files[src]; ok {
		f.files[dst] = data
		delete(f.files, src)
	}
	f.renamed = append(f.renamed, [2]string{src, dst})
	return nil
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
	a.EnrollFunc = func(_, _, _ string, _ []string, _, keyFile, _, output string) (string, error) {
		keyData, _ := os.ReadFile(keyFile)
		// output is connext_artifacts/ under the device slot; mtls_artifacts/ is alongside it.
		slotDir := filepath.Dir(strings.TrimSuffix(output, string(os.PathSeparator)))
		mtlsDir := filepath.Join(slotDir, "mtls_artifacts")
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "device.key"), keyData, 0o600)
		return "", nil
	}
	a.RequestIdentityFunc = func(_, _, _, _, _, _, _, output string) error {
		// Write a fake identity cert so renewIdentity can copy it to device.crt.
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "identity.crt"), []byte("FAKE-CERT"), 0o644)
		return nil
	}
	a.RequestPermissionsFunc = func(_, _, _, _, _, _, _ string) error { return nil }
	a.RequestPSKFunc = func(_, _, _, _, _, _ string) error { return nil }
	a.GetCRLFunc = func(_, _, _, _, _, _, _ string) error { return nil }
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
	a.Rename = ffs.Rename
	a.RemoveFile = func(path string) error {
		ffs.mu.Lock()
		defer ffs.mu.Unlock()
		delete(ffs.files, path)
		return nil
	}
	a.InboxDir = "/connext/inbox"
	a.ProcessedDir = "/connext/processed"
	a.FailedDir = "/connext/failed"
	a.PollInterval = 50 * time.Millisecond
	a.SweepInterval = 50 * time.Millisecond
	a.DeriveDeviceURLFunc = func(serviceID string) string {
		return "https://test.devices.cloud.dev-rti.com"
	}
	return a
}

// leaseJSON builds a minimal *_lease.json payload.
func leaseJSON(notBefore, notAfter time.Time) []byte {
	type leasePayload struct {
		Lease struct {
			NotBefore time.Time `json:"not_before"`
			NotAfter  time.Time `json:"not_after"`
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
		State: StateActive,
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
	ffs.WriteFile("/connext/svc1/part1/dev1/agent_state.json", data, 0o644)
	ffs.MkdirAll("/connext/svc1/part1/dev1", 0o755)
	ffs.MkdirAll("/connext/svc1/part1", 0o755)
	ffs.MkdirAll("/connext/svc1", 0o755)

	a.rehydrate()

	val, ok := a.profiles.Load(profileKey("svc1", "part1", "dev1"))
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

	ffs.WriteFile("/connext/svc1/part1/dev1/agent_state.json", []byte("not json"), 0o644)
	ffs.MkdirAll("/connext/svc1/part1/dev1", 0o755)
	ffs.MkdirAll("/connext/svc1/part1", 0o755)
	ffs.MkdirAll("/connext/svc1", 0o755)

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
	a.RequestIdentityFunc = func(_, _, _, _, _, _, _, _ string) error {
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
	a.RequestPermissionsFunc = func(_, _, _, _, _, _, _ string) error {
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
	// Write device URL so ResolveDeviceURL succeeds.
	ffs.WriteFile("/connext/svc/part/dev/device_url", []byte("https://svc.devices.example.com"), 0o644)
	p.mu.Lock()
	// notAfter in the past — threshold already passed.
	p.notAfter[ArtifactPermissions] = now.Add(-10 * time.Second)
	p.mu.Unlock()

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
	a.EnrollFunc = func(_, _, _ string, _ []string, _, keyFile, _, output string) (string, error) {
		keyData, _ := os.ReadFile(keyFile)
		// output is connext_artifacts/ under the device slot; mtls_artifacts/ is alongside it.
		slotDir := filepath.Dir(strings.TrimSuffix(output, string(os.PathSeparator)))
		mtlsDir := filepath.Join(slotDir, "mtls_artifacts")
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "device.key"), keyData, 0o600)
		enrolled <- struct{}{}
		return "", nil
	}
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	req := EnrollRequest{
		ServiceID:     "svc1",
		ParticipantID: "part1",
		CampaignToken: "tok",
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

	// File should have been renamed to processed/.
	found := false
	for _, pair := range ffs.renamed {
		if pair[0] == inboxFile && strings.HasPrefix(pair[1], "/connext/processed/") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("inbox file not moved to processed; renames: %v", ffs.renamed)
	}
}

func TestDrainInbox_RejectsInvalidJSON(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	inboxFile := "/connext/inbox/enroll-bad.json"
	ffs.WriteFile(inboxFile, []byte("{not json"), 0o644)

	a.drainInbox()

	movedToFailed := false
	for _, pair := range ffs.renamed {
		if pair[0] == inboxFile && strings.HasPrefix(pair[1], "/connext/failed/") {
			movedToFailed = true
			break
		}
	}
	if !movedToFailed {
		t.Fatalf("bad file not moved to failed; renames: %v", ffs.renamed)
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

	movedToFailed := false
	for _, pair := range ffs.renamed {
		if pair[0] == inboxFile && strings.HasPrefix(pair[1], "/connext/failed/") {
			movedToFailed = true
			break
		}
	}
	if !movedToFailed {
		t.Fatal("incomplete request not moved to failed")
	}
}

func TestDrainInbox_SkipsTmpFiles(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	enrollCalled := false
	a.EnrollFunc = func(_, _, _ string, _ []string, _, _, _, _ string) (string, error) {
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

	a.EnrollFunc = func(_, _, _ string, _ []string, _, _, _, _ string) (string, error) {
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

	// processInboxFile launches enrollProfile in the same goroutine, so by the
	// time drainInbox returns the file has been moved.
	movedToFailed := false
	for _, pair := range ffs.renamed {
		if pair[0] == inboxFile && strings.HasPrefix(pair[1], "/connext/failed/") {
			movedToFailed = true
			break
		}
	}
	if !movedToFailed {
		t.Fatalf("failed enrollment not moved to failed dir; renames: %v", ffs.renamed)
	}
}

// ─── State persistence ────────────────────────────────────────────────────────

func TestPersistState_WritesAgentStateJSON(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	now := time.Now().Truncate(time.Second)
	p := a.getOrCreateProfile("svc", "part", "dev")
	p.mu.Lock()
	p.state = StateActive
	p.notAfter[ArtifactIdentity] = now.Add(100 * time.Second)
	p.notAfter[ArtifactPermissions] = now.Add(200 * time.Second)
	p.mu.Unlock()

	if err := a.persistState(p); err != nil {
		t.Fatalf("persistState: %v", err)
	}

	data, err := ffs.ReadFile("/connext/svc/part/dev/agent_state.json")
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
	a.EnrollFunc = func(_, _, _ string, _ []string, _, keyFile, _, output string) (string, error) {
		// Simulate EnrollDevice writing the device key to the mTLS store.
		keyData, _ := os.ReadFile(keyFile)
		// output is connext_artifacts/ under the device slot; mtls_artifacts/ is alongside it.
		slotDir := filepath.Dir(strings.TrimSuffix(output, string(os.PathSeparator)))
		mtlsDir := filepath.Join(slotDir, "mtls_artifacts")
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "device.key"), keyData, 0o600)
		ffs.WriteFile(filepath.Join(mtlsDir, "device.crt"), []byte("FAKE-CERT"), 0o644)
		ffs.WriteFile(filepath.Join(mtlsDir, "ca-chain.pem"), []byte("FAKE-CA"), 0o644)
		return "", nil
	}
	a.RequestIdentityFunc = func(_, _, _, _, _, _, _, output string) error {
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
		Serial:        "SN-001",
		MACs:          []string{"AA:BB:CC:DD:EE:01"},
		DeviceName:    "dev",
	}
	if err := a.enrollProfile(req); err != nil {
		t.Fatalf("enrollProfile: %v", err)
	}

	val, ok := a.profiles.Load(profileKey("svc", "part", "dev"))
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

func TestEnrollProfile_EnrollErrorLeavesUnregistered(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.EnrollFunc = func(_, _, _ string, _ []string, _, _, _, _ string) (string, error) {
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
	stateData, _ := json.Marshal(AgentState{State: StateActive, NotAfter: map[ArtifactID]time.Time{}, IssuedAt: map[ArtifactID]time.Time{}, DeviceName: "dev"})
	_ = ffs.MkdirAll("/connext/svc", 0o755)
	_ = ffs.MkdirAll("/connext/svc/part", 0o755)
	_ = ffs.MkdirAll("/connext/svc/part/dev", 0o755)
	_ = ffs.WriteFile("/connext/svc/part/dev/agent_state.json", stateData, 0o644)
	a := buildTestAgent(t, ffs)
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
	stateData, _ := json.Marshal(AgentState{State: StateActive, NotAfter: map[ArtifactID]time.Time{}, IssuedAt: map[ArtifactID]time.Time{}, DeviceName: "dev"})
	_ = ffs.MkdirAll("/connext/svc", 0o755)
	_ = ffs.MkdirAll("/connext/svc/part", 0o755)
	_ = ffs.MkdirAll("/connext/svc/part/dev", 0o755)
	_ = ffs.WriteFile("/connext/svc/part/dev/agent_state.json", stateData, 0o644)
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	enrolled := make(chan struct{}, 1)
	a.EnrollFunc = func(_, _, _ string, _ []string, _, keyFile, _, output string) (string, error) {
		keyData, _ := os.ReadFile(keyFile)
		// output is connext_artifacts/ under the device slot; mtls_artifacts/ is alongside it.
		slotDir := filepath.Dir(strings.TrimSuffix(output, string(os.PathSeparator)))
		mtlsDir := filepath.Join(slotDir, "mtls_artifacts")
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "device.key"), keyData, 0o600)
		select {
		case enrolled <- struct{}{}:
		default:
		}
		return "", nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = a.Run(ctx) }()

	// Give the Run loop a moment to start, then drop an inbox file.
	time.Sleep(20 * time.Millisecond)
	req := EnrollRequest{
		ServiceID: "svc", ParticipantID: "part", Serial: "SN-001",
		MACs: []string{"AA:BB:CC:DD:EE:01"},
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

// ─── moveInboxFile ────────────────────────────────────────────────────────────

func TestMoveInboxFile_WritesResultSidecar(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	src := "/connext/inbox/enroll-x.json"
	ffs.WriteFile(src, []byte("{}"), 0o644)

	a.moveInboxFile(src, "/connext/failed", "something went wrong")

	dst := "/connext/failed/enroll-x.json.result.json"
	data, err := ffs.ReadFile(dst)
	if err != nil {
		t.Fatalf("result sidecar not written: %v", err)
	}
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("sidecar unmarshal: %v", err)
	}
	if result["error"] != "something went wrong" {
		t.Fatalf("unexpected sidecar content: %v", result)
	}
}

func TestMoveInboxFile_NoSidecarOnSuccess(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	src := "/connext/inbox/enroll-ok.json"
	ffs.WriteFile(src, []byte("{}"), 0o644)

	a.moveInboxFile(src, "/connext/processed", "")

	// No sidecar should be written.
	if _, err := ffs.ReadFile(src + ".result.json"); err == nil {
		t.Fatal("unexpected result sidecar for successful move")
	}
}

// ─── Device cert renewal ──────────────────────────────────────────────────────

func TestRenewArtifact_DeviceCert_Success(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	// Fake PEM key in the mtls_artifacts slot.
	mtlsDir := "/connext/svc/part/dev/mtls_artifacts"
	ffs.MkdirAll(mtlsDir, 0o755)
	ffs.WriteFile(filepath.Join(mtlsDir, "device.key"), []byte("KEY"), 0o600)
	ffs.WriteFile(filepath.Join(mtlsDir, "device_url"), []byte("https://svc.devices.example.com"), 0o644)
	ffs.WriteFile("/connext/svc/part/dev/device_url", []byte("https://svc.devices.example.com"), 0o644)

	// Build a minimal self-signed cert to act as the renewed device cert.
	notAfterWant := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	certPEM := buildFakeCertPEM(t, notAfterWant)

	renewCalled := make(chan struct{}, 1)
	a.RenewDeviceCertFunc = func(_, _, _, _, _, csrFile string, _ int, output string) error {
		// Write the fake renewed cert where the agent expects it.
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "device.crt"), certPEM, 0o644)
		ffs.WriteFile(filepath.Join(dir, "ca-chain.pem"), []byte("CA"), 0o644)
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
