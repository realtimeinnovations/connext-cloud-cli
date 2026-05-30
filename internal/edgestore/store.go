package edgestore

import (
	"os"
	"path/filepath"
	"strings"
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
	DeviceCertPEM []byte // written to mtls_artifacts/device.crt  (0644)
	CAChainPEM    []byte // written to mtls_artifacts/ca-chain.pem (0644)
	PrivateKeyPEM []byte // written to mtls_artifacts/device.key   (0600)
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

// SlotDir returns the root directory for a service+participant pair.
func (s *Store) SlotDir(serviceID, participantID string) string {
	return filepath.Join(s.BaseDir, serviceID, participantID)
}

// MTLSDir returns the directory that holds the mTLS credentials.
func (s *Store) MTLSDir(serviceID, participantID string) string {
	return filepath.Join(s.SlotDir(serviceID, participantID), "mtls_artifacts")
}

// ConnextArtifactsDir returns the directory that holds the DDS/Connext artifacts.
func (s *Store) ConnextArtifactsDir(serviceID, participantID string) string {
	return filepath.Join(s.SlotDir(serviceID, participantID), "connext_artifacts")
}

// DeviceCertPath is the mTLS leaf certificate path.
func (s *Store) DeviceCertPath(serviceID, participantID string) string {
	return filepath.Join(s.MTLSDir(serviceID, participantID), "device.crt")
}

// PrivateKeyPath is the device private key path.
func (s *Store) PrivateKeyPath(serviceID, participantID string) string {
	return filepath.Join(s.MTLSDir(serviceID, participantID), "device.key")
}

// CAChainPath is the Provisioning Service CA chain path.
func (s *Store) CAChainPath(serviceID, participantID string) string {
	return filepath.Join(s.MTLSDir(serviceID, participantID), "ca-chain.pem")
}

// WriteArtifacts persists enrollment artifacts into the correct slot,
// creating the directory structure as needed.
// Fields with nil or empty bytes are silently skipped.
func (s *Store) WriteArtifacts(serviceID, participantID string, a EnrollArtifacts) error {
	mtlsDir := s.MTLSDir(serviceID, participantID)
	connextDir := s.ConnextArtifactsDir(serviceID, participantID)

	if err := s.MkdirAll(mtlsDir, 0o755); err != nil {
		return err
	}
	if err := s.MkdirAll(connextDir, 0o755); err != nil {
		return err
	}

	if len(a.DeviceCertPEM) > 0 {
		if err := s.WriteFile(s.DeviceCertPath(serviceID, participantID), a.DeviceCertPEM, 0o644); err != nil {
			return err
		}
	}
	if len(a.CAChainPEM) > 0 {
		if err := s.WriteFile(s.CAChainPath(serviceID, participantID), a.CAChainPEM, 0o644); err != nil {
			return err
		}
	}
	if len(a.PrivateKeyPEM) > 0 {
		if err := s.WriteFile(s.PrivateKeyPath(serviceID, participantID), a.PrivateKeyPEM, 0o600); err != nil {
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
// serviceID or participantID being empty is a no-op.
func (s *Store) ResolveMTLSDefaults(serviceID, participantID, cert, key, ca string) (string, string, string) {
	if serviceID == "" || participantID == "" {
		return cert, key, ca
	}
	if cert == "" {
		if p := s.DeviceCertPath(serviceID, participantID); s.fileExists(p) {
			cert = p
		}
	}
	if key == "" {
		if p := s.PrivateKeyPath(serviceID, participantID); s.fileExists(p) {
			key = p
		}
	}
	if ca == "" {
		if p := s.CAChainPath(serviceID, participantID); s.fileExists(p) {
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
func (s *Store) DeviceURLPath(serviceID, participantID string) string {
	return filepath.Join(s.SlotDir(serviceID, participantID), "device_url")
}

// WriteDeviceURL persists the device endpoint URL for a slot, creating the
// slot directory as needed.  An empty url is silently ignored.
func (s *Store) WriteDeviceURL(serviceID, participantID, deviceURL string) error {
	if deviceURL == "" {
		return nil
	}
	if err := s.MkdirAll(s.SlotDir(serviceID, participantID), 0o755); err != nil {
		return err
	}
	return s.WriteFile(s.DeviceURLPath(serviceID, participantID), []byte(deviceURL), 0o644)
}

// ResolveDeviceURL returns the stored device endpoint URL for a slot,
// or "" if none has been saved yet.
func (s *Store) ResolveDeviceURL(serviceID, participantID string) string {
	p := s.DeviceURLPath(serviceID, participantID)
	if !s.fileExists(p) {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
