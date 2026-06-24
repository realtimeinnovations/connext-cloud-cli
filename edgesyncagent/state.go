// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"time"
)

// ─── Lease parsing & state persistence ───────────────────────────────────────

// readLease reads a *_lease.json file and returns both not_before and not_after.
// Returns zero times if the file cannot be read or a field is absent.
func (a *Agent) readLease(path string) (notBefore, notAfter time.Time) {
	data, err := a.ReadFile(path)
	if err != nil {
		return
	}
	var wrapper struct {
		Lease struct {
			NotBefore time.Time `json:"notBefore"`
			NotAfter  time.Time `json:"notAfter"`
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
	for _, key := range []string{"pskA", "pskB", "psk"} {
		slotData, ok := raw[key]
		if !ok {
			continue
		}
		var slot struct {
			Lease struct {
				NotAfter time.Time `json:"notAfter"`
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
