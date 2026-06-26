// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgestore

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store manages the local artifact cache under BaseDir.
// All file I/O fields are injectable for testing.
type Store struct {
	BaseDir   string
	WriteFile func(path string, data []byte, perm os.FileMode) error
	MkdirAll  func(path string, perm os.FileMode) error
	Stat      func(path string) (os.FileInfo, error)
}

// EnrollArtifacts holds the security material returned by a successful enrollment.
type EnrollArtifacts struct {
	DeviceCertPEM []byte // written to mtls_artifacts/node.crt  (0644)
	CAChainPEM    []byte // written to mtls_artifacts/ca-chain.crt (0644) and, as identity_ca.crt + permissions_ca.crt, to connext_artifacts/ (0644)
	PrivateKeyPEM []byte // written to mtls_artifacts/node.key   (0600)
	GovernanceP7S []byte // written to connext_artifacts/governance.p7s (0644)
}

// New creates a Store rooted at baseDir with real OS file operations.
func New(baseDir string) *Store {
	return &Store{
		BaseDir:   baseDir,
		WriteFile: os.WriteFile,
		MkdirAll:  os.MkdirAll,
		Stat:      os.Stat,
	}
}

// SlotDir returns the root directory for a serial+domain_template+participant triple.
//
// Layout:
//
//	.connext/<serial>/<domain_template_id>/<participant_id>/
func (s *Store) SlotDir(serial, domainTemplateID, participantID string) string {
	return filepath.Join(s.BaseDir, serial, domainTemplateID, participantID)
}

// MTLSDir returns the directory that holds the mTLS credentials.
func (s *Store) MTLSDir(serial, domainTemplateID, participantID string) string {
	return filepath.Join(s.SlotDir(serial, domainTemplateID, participantID), "mtls_artifacts")
}

// ConnextArtifactsDir returns the directory that holds the DDS/Connext artifacts.
func (s *Store) ConnextArtifactsDir(serial, domainTemplateID, participantID string) string {
	return filepath.Join(s.SlotDir(serial, domainTemplateID, participantID), "connext_artifacts")
}

// DeviceCertPath is the mTLS leaf certificate path.
func (s *Store) DeviceCertPath(serial, domainTemplateID, participantID string) string {
	return filepath.Join(s.MTLSDir(serial, domainTemplateID, participantID), "node.crt")
}

// PrivateKeyPath is the device private key path.
func (s *Store) PrivateKeyPath(serial, domainTemplateID, participantID string) string {
	return filepath.Join(s.MTLSDir(serial, domainTemplateID, participantID), "node.key")
}

// CAChainPath is the Provisioning Service CA chain path.
func (s *Store) CAChainPath(serial, domainTemplateID, participantID string) string {
	return filepath.Join(s.MTLSDir(serial, domainTemplateID, participantID), "ca-chain.crt")
}

// WriteArtifacts persists enrollment artifacts into the correct slot,
// creating the directory structure as needed.
// Fields with nil or empty bytes are silently skipped.
func (s *Store) WriteArtifacts(serial, domainTemplateID, participantID string, a EnrollArtifacts) error {
	mtlsDir := s.MTLSDir(serial, domainTemplateID, participantID)
	connextDir := s.ConnextArtifactsDir(serial, domainTemplateID, participantID)

	if err := s.MkdirAll(mtlsDir, 0o755); err != nil {
		return err
	}
	if err := s.MkdirAll(connextDir, 0o755); err != nil {
		return err
	}

	if len(a.DeviceCertPEM) > 0 {
		if err := s.WriteFile(s.DeviceCertPath(serial, domainTemplateID, participantID), a.DeviceCertPEM, 0o644); err != nil {
			return err
		}
	}
	if len(a.CAChainPEM) > 0 {
		if err := s.WriteFile(s.CAChainPath(serial, domainTemplateID, participantID), a.CAChainPEM, 0o644); err != nil {
			return err
		}
		if err := s.WriteFile(filepath.Join(connextDir, "identity_ca.crt"), a.CAChainPEM, 0o644); err != nil {
			return err
		}
		if err := s.WriteFile(filepath.Join(connextDir, "permissions_ca.crt"), a.CAChainPEM, 0o644); err != nil {
			return err
		}
	}
	if len(a.PrivateKeyPEM) > 0 {
		if err := s.WriteFile(s.PrivateKeyPath(serial, domainTemplateID, participantID), a.PrivateKeyPEM, 0o600); err != nil {
			return err
		}
	}
	if len(a.GovernanceP7S) > 0 {
		dest := filepath.Join(connextDir, "governance.p7s")
		if err := s.WriteFile(dest, a.GovernanceP7S, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ResolveMTLSDefaults fills cert, key, and ca from the store when the caller
// did not supply them and the stored files exist on disk.
// domainTemplateID or participantID being empty is a no-op.
func (s *Store) ResolveMTLSDefaults(serial, domainTemplateID, participantID, cert, key, ca string) (string, string, string) {
	if domainTemplateID == "" || participantID == "" {
		return cert, key, ca
	}
	if cert == "" {
		if p := s.DeviceCertPath(serial, domainTemplateID, participantID); s.fileExists(p) {
			cert = p
		}
	}
	if key == "" {
		if p := s.PrivateKeyPath(serial, domainTemplateID, participantID); s.fileExists(p) {
			key = p
		}
	}
	if ca == "" {
		if p := s.CAChainPath(serial, domainTemplateID, participantID); s.fileExists(p) {
			ca = p
		}
	}
	return cert, key, ca
}

func (s *Store) fileExists(path string) bool {
	_, err := s.Stat(path)
	return err == nil
}

// DeviceURLPath is the path of the stored device endpoint URL for a slot.
func (s *Store) DeviceURLPath(serial, domainTemplateID, participantID string) string {
	return filepath.Join(s.SlotDir(serial, domainTemplateID, participantID), "node_url")
}

// WriteDeviceURL persists the device endpoint URL for a slot, creating the
// slot directory as needed.  An empty url is silently ignored.
func (s *Store) WriteDeviceURL(serial, domainTemplateID, participantID, deviceURL string) error {
	if deviceURL == "" {
		return nil
	}
	if err := s.MkdirAll(s.SlotDir(serial, domainTemplateID, participantID), 0o755); err != nil {
		return err
	}
	return s.WriteFile(s.DeviceURLPath(serial, domainTemplateID, participantID), []byte(deviceURL), 0o644)
}

// ResolveDeviceURL returns the stored device endpoint URL for a slot,
// or "" if none has been saved yet.
func (s *Store) ResolveDeviceURL(serial, domainTemplateID, participantID string) string {
	p := s.DeviceURLPath(serial, domainTemplateID, participantID)
	if !s.fileExists(p) {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SlotInfo describes a single enrolled slot that has a stored node_url.
type SlotInfo struct {
	DomainTemplateID string
	ParticipantID    string
	DeviceURL        string
	EnrolledAt       time.Time // mtime of the node_url file; zero if unknown
}

// ListSlotsWithURL returns every slot under serial that has a stored
// node_url.  Returns nil when the serial directory does not exist or
// cannot be read.
func (s *Store) ListSlotsWithURL(serial string) []SlotInfo {
	serialDir := filepath.Join(s.BaseDir, serial)
	domainDirs, err := os.ReadDir(serialDir)
	if err != nil {
		return nil
	}
	var slots []SlotInfo
	for _, domainEntry := range domainDirs {
		if !domainEntry.IsDir() {
			continue
		}
		domainID := domainEntry.Name()
		participantDirs, err := os.ReadDir(filepath.Join(serialDir, domainID))
		if err != nil {
			continue
		}
		for _, partEntry := range participantDirs {
			if !partEntry.IsDir() {
				continue
			}
			participantID := partEntry.Name()
			urlPath := s.DeviceURLPath(serial, domainID, participantID)
			fi, statErr := os.Stat(urlPath)
			if statErr != nil {
				continue
			}
			data, readErr := os.ReadFile(urlPath)
			if readErr != nil {
				continue
			}
			u := strings.TrimSpace(string(data))
			if u == "" {
				continue
			}
			slots = append(slots, SlotInfo{
				DomainTemplateID: domainID,
				ParticipantID:    participantID,
				DeviceURL:        u,
				EnrolledAt:       fi.ModTime(),
			})
		}
	}
	return slots
}
