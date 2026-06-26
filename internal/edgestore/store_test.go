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
	if got := s.CAChainPath("SN-001", "dom", "p1"); got != filepath.Join("/base", "SN-001", "dom", "p1", "mtls_artifacts", "ca-chain.crt") {
		t.Fatalf("unexpected CAChainPath: %s", got)
	}
}

func TestLayeredPaths_Default(t *testing.T) {
	s := New("/base")
	const (
		svc  = "edge-prov"
		dom  = "0:domain-0849"
		part = "participant-sensors-0849"
		node = "b9a00ae9a51d4086b52dc96015e4c5b0"
	)
	// Agent base.
	connextRoot := filepath.Join("/base", "agent", "connext_artifacts", svc)
	mtlsNode := filepath.Join("/base", "agent", "mtls_artifacts", svc, dom, part, node)
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"AgentDir", s.AgentDir(), filepath.Join("/base", "agent")},
		{"InboxDir", s.InboxDir(), filepath.Join("/base", "agent", "inbox")},
		{"LogPath", s.LogPath(), filepath.Join("/base", "agent", "rticloud-edge-agent.log")},

		{"ServiceDir", s.ServiceDir(svc), connextRoot},
		{"IdentityCAPath", s.IdentityCAPath(svc), filepath.Join(connextRoot, "identity_ca.crt")},
		{"PermissionsCAPath", s.PermissionsCAPath(svc), filepath.Join(connextRoot, "permissions_ca.crt")},

		{"DomainDir", s.DomainDir(svc, dom), filepath.Join(connextRoot, dom)},
		{"GovernancePath", s.GovernancePath(svc, dom), filepath.Join(connextRoot, dom, "signed_governance.p7s")},
		{"CRLPath", s.CRLPath(svc, dom), filepath.Join(connextRoot, dom, "crl.pem")},

		{"NodeDir", s.NodeDir(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node)},
		{"IdentityCertPath", s.IdentityCertPath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "identity.crt")},
		{"IdentityKeyPath", s.IdentityKeyPath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "identity_key.pem")},
		{"IdentityLeasePath", s.IdentityLeasePath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "identity_lease.json")},
		{"PermissionsPath", s.PermissionsPath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "signed_permissions.p7s")},
		{"PermissionsLeasePath", s.PermissionsLeasePath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "permissions_lease.json")},

		{"NodeAgentDir", s.NodeAgentDir(svc, dom, part, node), mtlsNode},
		{"NodeCertPath", s.NodeCertPath(svc, dom, part, node), filepath.Join(mtlsNode, "node.crt")},
		{"NodeKeyPath", s.NodeKeyPath(svc, dom, part, node), filepath.Join(mtlsNode, "node.key")},
		{"NodeCAChainPath", s.NodeCAChainPath(svc, dom, part, node), filepath.Join(mtlsNode, "ca-chain.crt")},
		{"NodeURLPath", s.NodeURLPath(svc, dom, part, node), filepath.Join(mtlsNode, "node_url")},
		{"NodeStatePath", s.NodeStatePath(svc, dom, part, node), filepath.Join(mtlsNode, "agent_state.json")},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, c.got, c.want)
		}
	}
}

func TestLayeredPaths_ConnextDirOverride(t *testing.T) {
	s := New("/base")
	s.ConnextDir = "/rafa"
	const (
		svc  = "edge-prov"
		dom  = "0:domain-0849"
		part = "participant-sensors-0849"
		node = "b9a00ae9a51d4086b52dc96015e4c5b0"
	)
	// ConnextDir replaces the <service> root: CAs land directly under /rafa.
	if got := s.ServiceDir(svc); got != "/rafa" {
		t.Fatalf("ServiceDir with ConnextDir: got %s, want /rafa", got)
	}
	if got := s.IdentityCAPath(svc); got != filepath.Join("/rafa", "identity_ca.crt") {
		t.Fatalf("IdentityCAPath with ConnextDir: got %s", got)
	}
	if got := s.NodeDir(svc, dom, part, node); got != filepath.Join("/rafa", dom, part, node) {
		t.Fatalf("NodeDir with ConnextDir: got %s", got)
	}
	// The agent base (mtls + state) is unaffected by ConnextDir.
	wantMTLS := filepath.Join("/base", "agent", "mtls_artifacts", svc, dom, part, node)
	if got := s.NodeAgentDir(svc, dom, part, node); got != wantMTLS {
		t.Fatalf("NodeAgentDir must ignore ConnextDir: got %s, want %s", got, wantMTLS)
	}
	if got := s.InboxDir(); got != filepath.Join("/base", "agent", "inbox") {
		t.Fatalf("InboxDir must ignore ConnextDir: got %s", got)
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
	check(filepath.Join(s.ConnextArtifactsDir("SN-001", "svc1", "pp1"), "identity_ca.crt"), "CHAIN")
	check(filepath.Join(s.ConnextArtifactsDir("SN-001", "svc1", "pp1"), "permissions_ca.crt"), "CHAIN")
	check(s.PrivateKeyPath("SN-001", "svc1", "pp1"), "KEY")
	check(filepath.Join(s.ConnextArtifactsDir("SN-001", "svc1", "pp1"), "signed_governance.p7s"), "GOV")

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
		t.Fatalf("ca-chain.crt should exist: %v", err)
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
