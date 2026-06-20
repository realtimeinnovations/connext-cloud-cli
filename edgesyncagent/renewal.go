package edgesyncagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
