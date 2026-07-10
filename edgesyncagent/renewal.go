// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
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
	a.claimDomainOwner(p)
	owner := a.isDomainOwner(p)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := a.Now()
	for _, artifact := range allArtifacts {
		// Domain-scoped artifacts are scheduled only by the domain owner.
		if isDomainArtifact(artifact) && !owner {
			continue
		}
		notAfter, ok := p.notAfter[artifact]
		if !ok || notAfter.IsZero() {
			continue
		}
		a.scheduleArtifactLocked(p, artifact, now, notAfter)
	}
	if owner {
		a.schedulePSKPhasesLocked(p, now)
	}
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
// domainTemplateID is the profile's domain template id (p.domain()).
func (a *Agent) RenewArtifact(domainTemplateID, participantID, deviceName string, art ArtifactID) error {
	key := profileKey(domainTemplateID, participantID, deviceName)
	val, ok := a.profiles.Load(key)
	if !ok {
		return fmt.Errorf("profile not found: %s", key)
	}
	p := val.(*profile)
	// Domain-scoped artifacts (PSK, CRL) are managed by the domain owner; renew
	// them on the owner so the shared files are not touched concurrently.
	if isDomainArtifact(art) {
		if v, ok := a.domainOwners.Load(domainOwnerKey(p.service(), p.domain())); ok {
			p = v.(*profile)
		}
	}
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
	a.emitf(catRenewal, tui.LogInfo, "artifact renewal started service=%s participant=%s artifact=%s reason=%s",
		p.serviceID, p.participantID, artifact, reason)

	p.mu.Lock()
	oldNotAfter := p.notAfter[artifact]
	p.setState(StateRenewing)
	p.mu.Unlock()

	service, domain, participant, node := p.service(), p.domain(), p.participant(), p.node()
	url := a.Store.ResolveNodeURL(service, domain, participant, node)
	cert, key, ca := a.Store.ResolveNodeMTLS(service, domain, participant, node, "", "", "")
	nodeDir := a.Store.NodeDir(service, domain, participant, node)
	domainDir := a.Store.DomainDir(service, domain)
	nodeOut := nodeDir + string(os.PathSeparator)
	domainOut := domainDir + string(os.PathSeparator)

	var newNotAfter time.Time
	var newNotBefore time.Time
	var err error

	switch artifact {
	case ArtifactIdentity:
		newNotAfter, err = a.renewIdentity(p, url, cert, key, ca, nodeOut)
		if err == nil {
			newNotBefore, _ = a.readLease(filepath.Join(nodeDir, "identity.lease.json"))
		}
	case ArtifactPermissions:
		if err = a.RequestPermissionsFunc(url, cert, key, ca, "", nodeOut); err == nil {
			newNotBefore, newNotAfter = a.readLease(filepath.Join(nodeDir, "permissions.lease.json"))
		}
	case ArtifactPSK:
		var pskA, pskB pskSlotLease
		pskA, pskB, err = a.renewPSKAt80(p, url, cert, key, ca, domainOut)
		if err == nil {
			// Anchor issuedAt to sA's real validity start (lease not_before) so
			// the 80% point stays fixed across renewals instead of drifting.
			newNotAfter = pskA.notAfter
			newNotBefore = pskA.notBefore

			// Re-arm the PSK phase timers for the CURRENT primary.  At the 80%
			// mark the active seed (sA) has not rotated out yet — it expires at
			// newNotAfter (psk_a's notAfter, the 100% mark), so the rotation
			// must fire at newNotAfter.  Arming it for pskBNotAfter (sB's
			// expiry, a full key-period later) would cancel the imminent sA
			// rotation and delay the first rotation by an entire period.
			// pskRotate advances ArtifactPSKRotate to sB's expiry when sA
			// actually rotates out, and arms the next 80% renewal for sB.
			now2 := a.Now()
			p.mu.Lock()
			p.notAfter[ArtifactPSKRotate] = newNotAfter
			if p.pskBaseTTL > 0 {
				p.notAfter[ArtifactPSKCleanup] = newNotAfter.Add(p.pskBaseTTL / 5) // +20 %
			}
			// Advance the staged sB window for the next cycle.  Use sB's values
			// if available; fall back to sA's.
			if !pskB.notAfter.IsZero() {
				p.pskBNotAfter = pskB.notAfter
				p.pskBNotBefore = pskB.notBefore
			} else {
				p.pskBNotAfter = newNotAfter
				p.pskBNotBefore = newNotBefore
			}
			a.schedulePSKPhasesLocked(p, now2)
			p.mu.Unlock()
		}
	case ArtifactCRL:
		err = a.GetCRLFunc(url, cert, key, ca, "", domainOut)
		if err == nil {
			newNotAfter = a.Now().Add(a.CRLInterval)
		}
	case ArtifactDeviceCert:
		newNotAfter, err = a.renewDeviceCert(p, url, cert, key, ca)
	}

	if err != nil {
		a.emitf(catRenewal, tui.LogWarn, "artifact renewal failed service=%s participant=%s artifact=%s err=%v",
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

	a.emitf(catRenewal, tui.LogGood, "artifact renewal complete service=%s participant=%s artifact=%s old_not_after=%s new_not_after=%s",
		p.serviceID, p.participantID, artifact, oldNotAfter.Format(time.RFC3339), newNotAfter.Format(time.RFC3339))

	if err := a.persistState(p); err != nil {
		a.emitf(catWarning, tui.LogWarn, "Warning: could not persist agent state: %v", err)
	}

	// PSK renews once per key cycle, not on a repeating 80% threshold: the 80%
	// fetch has staged sB, and the rotate phase timer (armed above) owns the
	// 80%→100% window.  Re-arming a threshold timer here would re-fire
	// renewPSKAt80 in a tight loop until 100% (RenewalDelay is already 0 past
	// 80%).  pskRotate arms the next 80% renewal for sB once it rotates in.
	if artifact == ArtifactPSK {
		return
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
// node key (Store.NodeKeyPath) so the DDS Security identity credential and the
// provisioning-transport credential live in separate trust domains.
func (a *Agent) identityKeyPath(p *profile) string {
	return a.Store.IdentityKeyPath(p.service(), p.domain(), p.participant(), p.node())
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

	if err := a.RequestIdentityFunc(url, cert, key, ca, "", csrPath, output); err != nil {
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

	leasePath := filepath.Join(strings.TrimSuffix(output, string(os.PathSeparator)), "identity.lease.json")
	return a.readLeaseNotAfter(leasePath), nil
}

// renewDeviceCert renews the mTLS device certificate using the same key pair
// by calling POST /device/renew-cert.  The renewed certificate is saved to
// mtls_artifacts/node.crt and the new NotAfter is read from the certificate.
func (a *Agent) renewDeviceCert(p *profile, url, cert, key, ca string) (time.Time, error) {
	service, domain, participant, node := p.service(), p.domain(), p.participant(), p.node()
	existingKey, err := a.ReadFile(a.Store.NodeKeyPath(service, domain, participant, node))
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

	mtlsOutput := a.Store.NodeAgentDir(service, domain, participant, node) + string(os.PathSeparator)
	if err := a.RenewDeviceCertFunc(url, cert, key, ca, "", csrPath, 0, mtlsOutput); err != nil {
		return time.Time{}, err
	}

	return a.readCertNotAfter(a.Store.NodeCertPath(service, domain, participant, node)), nil
}
