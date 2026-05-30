package edgestore

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(t.TempDir())
}

func TestSlotPaths(t *testing.T) {
	s := New("/base")
	if got := s.SlotDir("svc", "p1"); got != filepath.Join("/base", "svc", "p1") {
		t.Fatalf("unexpected SlotDir: %s", got)
	}
	if got := s.MTLSDir("svc", "p1"); got != filepath.Join("/base", "svc", "p1", "mtls_artifacts") {
		t.Fatalf("unexpected MTLSDir: %s", got)
	}
	if got := s.ConnextArtifactsDir("svc", "p1"); got != filepath.Join("/base", "svc", "p1", "connext_artifacts") {
		t.Fatalf("unexpected ConnextArtifactsDir: %s", got)
	}
	if got := s.DeviceCertPath("svc", "p1"); got != filepath.Join("/base", "svc", "p1", "mtls_artifacts", "device.crt") {
		t.Fatalf("unexpected DeviceCertPath: %s", got)
	}
	if got := s.PrivateKeyPath("svc", "p1"); got != filepath.Join("/base", "svc", "p1", "mtls_artifacts", "device.key") {
		t.Fatalf("unexpected PrivateKeyPath: %s", got)
	}
	if got := s.CAChainPath("svc", "p1"); got != filepath.Join("/base", "svc", "p1", "mtls_artifacts", "ca-chain.pem") {
		t.Fatalf("unexpected CAChainPath: %s", got)
	}
}

func TestWriteArtifacts(t *testing.T) {
	s := newTestStore(t)
	arts := EnrollArtifacts{
		DeviceCertPEM: []byte("CERT"),
		CAChainPEM:    []byte("CHAIN"),
		PrivateKeyPEM: []byte("KEY"),
		GovernanceP7S: []byte("GOV"),
	}
	if err := s.WriteArtifacts("svc1", "pp1", arts); err != nil {
		t.Fatal(err)
	}

	check := func(path, want string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing file %s: %v", path, err)
		}
		if string(data) != want {
			t.Fatalf("file %s: got %q, want %q", path, string(data), want)
		}
	}

	check(s.DeviceCertPath("svc1", "pp1"), "CERT")
	check(s.CAChainPath("svc1", "pp1"), "CHAIN")
	check(s.PrivateKeyPath("svc1", "pp1"), "KEY")
	check(filepath.Join(s.ConnextArtifactsDir("svc1", "pp1"), "governance.p7s"), "GOV")

	// private key must have mode 0600
	info, err := os.Stat(s.PrivateKeyPath("svc1", "pp1"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("device.key mode: got %04o, want 0600", info.Mode().Perm())
	}
}

func TestWriteArtifactsSkipsEmpty(t *testing.T) {
	s := newTestStore(t)
	if err := s.WriteArtifacts("svc1", "pp1", EnrollArtifacts{CAChainPEM: []byte("CHAIN")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.DeviceCertPath("svc1", "pp1")); err == nil {
		t.Fatal("device.crt should not exist when DeviceCertPEM is empty")
	}
	if _, err := os.Stat(s.CAChainPath("svc1", "pp1")); err != nil {
		t.Fatalf("ca-chain.pem should exist: %v", err)
	}
}

func TestResolveMTLSDefaultsNoSlot(t *testing.T) {
	s := newTestStore(t)
	cert, key, ca := s.ResolveMTLSDefaults("", "", "", "", "")
	if cert != "" || key != "" || ca != "" {
		t.Fatal("expected no change when service/participant are empty")
	}
}

func TestResolveMTLSDefaultsFilesExist(t *testing.T) {
	s := newTestStore(t)
	_ = s.WriteArtifacts("svc1", "pp1", EnrollArtifacts{
		DeviceCertPEM: []byte("CERT"),
		CAChainPEM:    []byte("CHAIN"),
		PrivateKeyPEM: []byte("KEY"),
	})
	cert, key, ca := s.ResolveMTLSDefaults("svc1", "pp1", "", "", "")
	if cert != s.DeviceCertPath("svc1", "pp1") {
		t.Fatalf("unexpected cert: %s", cert)
	}
	if key != s.PrivateKeyPath("svc1", "pp1") {
		t.Fatalf("unexpected key: %s", key)
	}
	if ca != s.CAChainPath("svc1", "pp1") {
		t.Fatalf("unexpected ca: %s", ca)
	}
}

func TestResolveMTLSDefaultsExplicitFlagsPreserved(t *testing.T) {
	s := newTestStore(t)
	_ = s.WriteArtifacts("svc1", "pp1", EnrollArtifacts{
		DeviceCertPEM: []byte("CERT"),
		CAChainPEM:    []byte("CHAIN"),
		PrivateKeyPEM: []byte("KEY"),
	})
	cert, key, ca := s.ResolveMTLSDefaults("svc1", "pp1", "/my/cert.pem", "/my/key.pem", "/my/ca.pem")
	if cert != "/my/cert.pem" || key != "/my/key.pem" || ca != "/my/ca.pem" {
		t.Fatal("explicit flags should not be overridden by store defaults")
	}
}

func TestResolveMTLSDefaultsMissingFiles(t *testing.T) {
	s := newTestStore(t)
	cert, key, ca := s.ResolveMTLSDefaults("svc1", "pp1", "", "", "")
	if cert != "" || key != "" || ca != "" {
		t.Fatal("expected empty strings when no artifacts have been stored")
	}
}
