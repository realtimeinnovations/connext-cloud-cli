// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

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
	if got := s.SlotDir("SN-001", "dom", "p1"); got != filepath.Join("/base", "SN-001", "dom", "p1") {
		t.Fatalf("unexpected SlotDir: %s", got)
	}
	if got := s.MTLSDir("SN-001", "dom", "p1"); got != filepath.Join("/base", "SN-001", "dom", "p1", "mtls_artifacts") {
		t.Fatalf("unexpected MTLSDir: %s", got)
	}
	if got := s.ConnextArtifactsDir("SN-001", "dom", "p1"); got != filepath.Join("/base", "SN-001", "dom", "p1", "connext_artifacts") {
		t.Fatalf("unexpected ConnextArtifactsDir: %s", got)
	}
	if got := s.DeviceCertPath("SN-001", "dom", "p1"); got != filepath.Join("/base", "SN-001", "dom", "p1", "mtls_artifacts", "node.crt") {
		t.Fatalf("unexpected DeviceCertPath: %s", got)
	}
	if got := s.PrivateKeyPath("SN-001", "dom", "p1"); got != filepath.Join("/base", "SN-001", "dom", "p1", "mtls_artifacts", "node.key") {
		t.Fatalf("unexpected PrivateKeyPath: %s", got)
	}
	if got := s.CAChainPath("SN-001", "dom", "p1"); got != filepath.Join("/base", "SN-001", "dom", "p1", "mtls_artifacts", "ca-chain.pem") {
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
	if err := s.WriteArtifacts("SN-001", "svc1", "pp1", arts); err != nil {
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

	check(s.DeviceCertPath("SN-001", "svc1", "pp1"), "CERT")
	check(s.CAChainPath("SN-001", "svc1", "pp1"), "CHAIN")
	check(filepath.Join(s.ConnextArtifactsDir("SN-001", "svc1", "pp1"), "ca-chain.pem"), "CHAIN")
	check(s.PrivateKeyPath("SN-001", "svc1", "pp1"), "KEY")
	check(filepath.Join(s.ConnextArtifactsDir("SN-001", "svc1", "pp1"), "governance.p7s"), "GOV")

	// private key must have mode 0600
	info, err := os.Stat(s.PrivateKeyPath("SN-001", "svc1", "pp1"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("node.key mode: got %04o, want 0600", info.Mode().Perm())
	}
}

func TestWriteArtifactsSkipsEmpty(t *testing.T) {
	s := newTestStore(t)
	if err := s.WriteArtifacts("SN-001", "svc1", "pp1", EnrollArtifacts{CAChainPEM: []byte("CHAIN")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.DeviceCertPath("SN-001", "svc1", "pp1")); err == nil {
		t.Fatal("node.crt should not exist when DeviceCertPEM is empty")
	}
	if _, err := os.Stat(s.CAChainPath("SN-001", "svc1", "pp1")); err != nil {
		t.Fatalf("ca-chain.pem should exist: %v", err)
	}
}

func TestResolveMTLSDefaultsNoSlot(t *testing.T) {
	s := newTestStore(t)
	cert, key, ca := s.ResolveMTLSDefaults("", "", "", "", "", "")
	if cert != "" || key != "" || ca != "" {
		t.Fatal("expected no change when service/participant are empty")
	}
}

func TestResolveMTLSDefaultsFilesExist(t *testing.T) {
	s := newTestStore(t)
	_ = s.WriteArtifacts("SN-001", "svc1", "pp1", EnrollArtifacts{
		DeviceCertPEM: []byte("CERT"),
		CAChainPEM:    []byte("CHAIN"),
		PrivateKeyPEM: []byte("KEY"),
	})
	cert, key, ca := s.ResolveMTLSDefaults("SN-001", "svc1", "pp1", "", "", "")
	if cert != s.DeviceCertPath("SN-001", "svc1", "pp1") {
		t.Fatalf("unexpected cert: %s", cert)
	}
	if key != s.PrivateKeyPath("SN-001", "svc1", "pp1") {
		t.Fatalf("unexpected key: %s", key)
	}
	if ca != s.CAChainPath("SN-001", "svc1", "pp1") {
		t.Fatalf("unexpected ca: %s", ca)
	}
}

func TestResolveMTLSDefaultsExplicitFlagsPreserved(t *testing.T) {
	s := newTestStore(t)
	_ = s.WriteArtifacts("SN-001", "svc1", "pp1", EnrollArtifacts{
		DeviceCertPEM: []byte("CERT"),
		CAChainPEM:    []byte("CHAIN"),
		PrivateKeyPEM: []byte("KEY"),
	})
	cert, key, ca := s.ResolveMTLSDefaults("SN-001", "svc1", "pp1", "/my/cert.pem", "/my/key.pem", "/my/ca.pem")
	if cert != "/my/cert.pem" || key != "/my/key.pem" || ca != "/my/ca.pem" {
		t.Fatal("explicit flags should not be overridden by store defaults")
	}
}

func TestResolveMTLSDefaultsMissingFiles(t *testing.T) {
	s := newTestStore(t)
	cert, key, ca := s.ResolveMTLSDefaults("SN-001", "svc1", "pp1", "", "", "")
	if cert != "" || key != "" || ca != "" {
		t.Fatal("expected empty strings when no artifacts have been stored")
	}
}

func TestListSlotsWithURLEmpty(t *testing.T) {
	s := newTestStore(t)
	if got := s.ListSlotsWithURL("SN-999"); got != nil {
		t.Fatalf("expected nil for unknown serial, got %v", got)
	}
}

func TestListSlotsWithURLSingleSlot(t *testing.T) {
	s := newTestStore(t)
	if err := s.WriteDeviceURL("SN-001", "dom-1", "pp1", "https://device.example.com"); err != nil {
		t.Fatal(err)
	}
	slots := s.ListSlotsWithURL("SN-001")
	if len(slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(slots))
	}
	if slots[0].DomainTemplateID != "dom-1" {
		t.Errorf("unexpected DomainTemplateID: %s", slots[0].DomainTemplateID)
	}
	if slots[0].ParticipantID != "pp1" {
		t.Errorf("unexpected ParticipantID: %s", slots[0].ParticipantID)
	}
	if slots[0].DeviceURL != "https://device.example.com" {
		t.Errorf("unexpected DeviceURL: %s", slots[0].DeviceURL)
	}
	if slots[0].EnrolledAt.IsZero() {
		t.Error("expected non-zero EnrolledAt")
	}
}

func TestListSlotsWithURLMultipleSlots(t *testing.T) {
	s := newTestStore(t)
	if err := s.WriteDeviceURL("SN-001", "dom-1", "pp1", "https://a.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteDeviceURL("SN-001", "dom-2", "pp2", "https://b.example.com"); err != nil {
		t.Fatal(err)
	}
	// A slot without node_url should not appear.
	if err := s.WriteArtifacts("SN-001", "dom-3", "pp3", EnrollArtifacts{DeviceCertPEM: []byte("CERT")}); err != nil {
		t.Fatal(err)
	}
	slots := s.ListSlotsWithURL("SN-001")
	if len(slots) != 2 {
		t.Fatalf("expected 2 slots with URL, got %d", len(slots))
	}
}
