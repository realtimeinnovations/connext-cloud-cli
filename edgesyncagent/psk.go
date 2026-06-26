// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

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
	return a.Store.DomainDir(p.service(), p.domain())
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
	pskA, pskB := a.readPSKABLease(leasePath)

	p.mu.Lock()
	defer p.mu.Unlock()

	// Override the notAfter already set by enrollProfile with psk_a's expiry.
	// The 80% timer fires at 80% of sA's lifetime.  Anchor issuedAt to sA's
	// real validity start (lease not_before) rather than enrolledAt: the key
	// may have been minted before this agent picked it up, and a stable
	// issuedAt keeps the 80% point fixed across renewals.
	if !pskA.notAfter.IsZero() {
		p.notAfter[ArtifactPSK] = pskA.notAfter
		issuedAt := pskA.notBefore
		if issuedAt.IsZero() {
			issuedAt = enrolledAt
		}
		p.issuedAt[ArtifactPSK] = issuedAt
	}

	// PSKRotate fires at sA's notAfter (= 100% mark).
	if !pskA.notAfter.IsZero() {
		p.notAfter[ArtifactPSKRotate] = pskA.notAfter
	}

	baseTTL := pskA.notAfter.Sub(p.issuedAt[ArtifactPSK])
	if baseTTL > 0 {
		p.pskBaseTTL = baseTTL
		// PSKCleanup fires at 120% = sA.notAfter + 20% of baseTTL.
		p.notAfter[ArtifactPSKCleanup] = pskA.notAfter.Add(baseTTL / 5)
	}

	// Store sB's window so pskRotate can hand it to the next cycle.
	p.pskBNotAfter = pskB.notAfter
	p.pskBNotBefore = pskB.notBefore
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
// Returns psk_a's validity window (for the ArtifactPSK 80%-threshold timer)
// and psk_b's validity window (for advancing the staged-key state).
func (a *Agent) renewPSKAt80(p *profile, url, cert, key, ca, output string) (pskA, pskB pskSlotLease, err error) {
	outDir := strings.TrimSuffix(output, string(os.PathSeparator))
	primaryPath := pskPrimaryPath(outDir)
	extraPath := pskExtraPath(outDir)
	tempPath := pskTempPath(outDir)
	leasePath := pskLeasePath(outDir)

	// 1. Save the current seed before the server call so we can detect
	//    unexpected rotation and build the extra overlap if needed.
	savedPrimary, readErr := a.ReadFile(primaryPath)
	if readErr != nil {
		return pskSlotLease{}, pskSlotLease{}, fmt.Errorf("psk 80%%: reading psk_primary.txt: %w", readErr)
	}

	// 2. Call the server. RequestPSKFunc writes:
	//      psk_primary.txt = psk_a (active seed)
	//      psk_extra.txt   = psk_a + "\n" + psk_b (overlap pair)
	//      psk_lease.json  = leases for both slots
	if callErr := a.RequestPSKFunc(url, cert, key, ca, "", output); callErr != nil {
		return pskSlotLease{}, pskSlotLease{}, fmt.Errorf("psk 80%%: fetching next PSK: %w", callErr)
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
		a.emitf(catPSK, tui.LogInfo,
			"psk 80%%: WARNING: server primary (%q) differs from local primary (%q) — "+
				"server has already rotated; keeping server value service=%s participant=%s",
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
		return pskSlotLease{}, pskSlotLease{}, fmt.Errorf("psk 80%%: reading psk_extra.txt: %w", readExtraErr)
	}
	// Stage sB as a single line — pskRotate must never receive multi-line content.
	nextPrimary := pskFirstLine([]byte(pskSecondLine(extraData)))
	if err := a.WriteFile(tempPath, []byte(nextPrimary), 0o644); err != nil {
		a.emitf(catPSK, tui.LogWarn, "psk 80%%: writing psk_temp.txt: %v", err)
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

	// Return psk_a's window for the 80%-threshold timer and psk_b's window
	// so the caller can correctly advance the staged-key state for the next cycle.
	pskA, pskB = a.readPSKABLease(leasePath)
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
		a.emitf(catPSK, tui.LogWarn, "psk_rotate: reading psk_temp.txt service=%s participant=%s: %v",
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
		a.emitf(catPSK, tui.LogWarn, "psk_rotate: writing psk_primary.txt service=%s participant=%s: %v",
			p.serviceID, p.participantID, err)
		return
	}

	// Set extra to sA so peers still using the old primary can decrypt during
	// the 100%–120% overlap window.  extra ≠ primary by construction.
	if oldPrimary != "" {
		_ = a.WriteFile(pskExtraPath(outDir), []byte(oldPrimary), 0o644)
	}

	a.emitf(catPSK, tui.LogGood, "psk_rotate: seed rotated to sB service=%s participant=%s",
		p.serviceID, p.participantID)

	// sB is now the active primary.  Advance the ENTIRE ArtifactPSK window to
	// sB's lease so the TUI reflects the new key immediately — otherwise
	// ArtifactPSK still points at the stale sA expiry (now in the past) and the
	// row briefly shows a false "needs renewal" until the next renewal runs.
	// Also arm a fresh 80% renewal timer (and the rotate/cleanup phase timers)
	// against sB's real window so the cycle continues for the new key.
	now := a.Now()
	p.mu.Lock()
	if !p.pskBNotAfter.IsZero() {
		issuedAt := p.pskBNotBefore
		if issuedAt.IsZero() && p.pskBaseTTL > 0 {
			// Older persisted state predates pskBNotBefore: derive sB's start.
			issuedAt = p.pskBNotAfter.Add(-p.pskBaseTTL)
		}
		p.notAfter[ArtifactPSK] = p.pskBNotAfter
		if !issuedAt.IsZero() {
			p.issuedAt[ArtifactPSK] = issuedAt
		}
		p.notAfter[ArtifactPSKRotate] = p.pskBNotAfter
		if p.pskBaseTTL > 0 {
			p.notAfter[ArtifactPSKCleanup] = p.pskBNotAfter.Add(p.pskBaseTTL / 5) // +20 %
		}
		a.schedulePSKPhasesLocked(p, now)
		a.scheduleArtifactLocked(p, ArtifactPSK, now, p.pskBNotAfter)
	} else {
		// No staged sB known — clear the rotate marker.
		delete(p.notAfter, ArtifactPSKRotate)
	}
	p.mu.Unlock()

	if err := a.persistState(p); err != nil {
		a.emitf(catWarning, tui.LogWarn, "Warning: could not persist agent state after psk_rotate: %v", err)
	}
}

// pskCleanup executes the 120% phase: clears psk_extra.txt to close the
// overlap window.  At this point sA is expired and no peers should still be
// using it, so the overlap entry (set by pskRotate) is no longer needed.
func (a *Agent) pskCleanup(p *profile) {
	outDir := a.pskOutDir(p)
	if err := a.WriteFile(pskExtraPath(outDir), []byte{}, 0o644); err != nil {
		a.emitf(catPSK, tui.LogWarn, "psk_cleanup: clearing psk_extra.txt service=%s participant=%s: %v",
			p.serviceID, p.participantID, err)
		return
	}
	a.emitf(catPSK, tui.LogGood, "psk_cleanup: seed_extra cleared service=%s participant=%s",
		p.serviceID, p.participantID)

	p.mu.Lock()
	delete(p.notAfter, ArtifactPSKCleanup)
	p.mu.Unlock()

	if err := a.persistState(p); err != nil {
		a.emitf(catWarning, tui.LogWarn, "Warning: could not persist agent state after psk_cleanup: %v", err)
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

// pskSlotLease is the validity window of one PSK slot (pskA or pskB).
type pskSlotLease struct {
	notBefore time.Time
	notAfter  time.Time
}

// readPSKABLease reads the psk_a and psk_b validity windows from a
// psk_lease.json.  Both slots' not_before and not_after are returned so the
// caller can anchor issuedAt to the key's real window (not to "now").
func (a *Agent) readPSKABLease(path string) (pskA, pskB pskSlotLease) {
	data, err := a.ReadFile(path)
	if err != nil {
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	readSlot := func(key string) pskSlotLease {
		slotData, ok := raw[key]
		if !ok {
			return pskSlotLease{}
		}
		var slot struct {
			Lease struct {
				NotBefore time.Time `json:"notBefore"`
				NotAfter  time.Time `json:"notAfter"`
			} `json:"lease"`
		}
		if err := json.Unmarshal(slotData, &slot); err != nil {
			return pskSlotLease{}
		}
		return pskSlotLease{notBefore: slot.Lease.NotBefore, notAfter: slot.Lease.NotAfter}
	}
	pskA = readSlot("pskA")
	pskB = readSlot("pskB")
	return
}
