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

func TestLayeredPaths_Default(t *testing.T) {
	s := New("/base")
	const (
		svc  = "edge-prov"
		dom  = "0:domain-0849"
		part = "participant-sensors-0849"
		node = "b9a00ae9a51d4086b52dc96015e4c5b0"
	)
	// Agent base.
	connextRoot := filepath.Join("/base", "agent", svc, "connext_artifacts")
	domainRoot := filepath.Join(connextRoot, dom)
	mtlsNode := filepath.Join("/base", "agent", svc, "mtls_artifacts", dom, part, node)
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"AgentDir", s.AgentDir(), filepath.Join("/base", "agent")},
		{"InboxDir", s.InboxDir(), filepath.Join("/base", "agent", "inbox")},
		{"LogPath", s.LogPath(), filepath.Join("/base", "agent", "rticloud-edge-agent.log")},

		{"ServiceDir", s.ServiceDir(svc), connextRoot},
		{"IdentityCAPath", s.IdentityCAPath(svc, dom), filepath.Join(domainRoot, "identity_ca.crt")},
		{"PermissionsCAPath", s.PermissionsCAPath(svc, dom), filepath.Join(domainRoot, "permissions_ca.crt")},

		{"DomainDir", s.DomainDir(svc, dom), filepath.Join(connextRoot, dom)},
		{"GovernancePath", s.GovernancePath(svc, dom), filepath.Join(connextRoot, dom, "signed_governance.p7s")},
		{"CRLPath", s.CRLPath(svc, dom), filepath.Join(connextRoot, dom, "crl.pem")},

		{"NodeDir", s.NodeDir(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node)},
		{"IdentityCertPath", s.IdentityCertPath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "identity.crt")},
		{"IdentityKeyPath", s.IdentityKeyPath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "identity.key")},
		{"IdentityLeasePath", s.IdentityLeasePath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "identity.lease.json")},
		{"PermissionsPath", s.PermissionsPath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "signed_permissions.p7s")},
		{"PermissionsLeasePath", s.PermissionsLeasePath(svc, dom, part, node), filepath.Join(connextRoot, dom, part, node, "signed_permissions.lease.json")},

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
	// ConnextDir replaces the <service> root: CAs land under /rafa/<domain>.
	if got := s.ServiceDir(svc); got != "/rafa" {
		t.Fatalf("ServiceDir with ConnextDir: got %s, want /rafa", got)
	}
	if got := s.IdentityCAPath(svc, dom); got != filepath.Join("/rafa", dom, "identity_ca.crt") {
		t.Fatalf("IdentityCAPath with ConnextDir: got %s", got)
	}
	if got := s.NodeDir(svc, dom, part, node); got != filepath.Join("/rafa", dom, part, node) {
		t.Fatalf("NodeDir with ConnextDir: got %s", got)
	}
	// The agent base (mtls + state) is unaffected by ConnextDir.
	wantMTLS := filepath.Join("/base", "agent", svc, "mtls_artifacts", dom, part, node)
	if got := s.NodeAgentDir(svc, dom, part, node); got != wantMTLS {
		t.Fatalf("NodeAgentDir must ignore ConnextDir: got %s, want %s", got, wantMTLS)
	}
	if got := s.InboxDir(); got != filepath.Join("/base", "agent", "inbox") {
		t.Fatalf("InboxDir must ignore ConnextDir: got %s", got)
	}
}

func TestWriteEnrollArtifacts_Layered(t *testing.T) {
	s := newTestStore(t)
	const (
		svc  = "edge-prov"
		dom  = "0:domain-0849"
		part = "participant-sensors-0849"
		node = "SN-001"
	)
	arts := EnrollArtifacts{
		DeviceCertPEM: []byte("CERT"),
		CAChainPEM:    []byte("CHAIN"),
		PrivateKeyPEM: []byte("KEY"),
		GovernanceP7S: []byte("GOV"),
	}
	if err := s.WriteEnrollArtifacts(svc, dom, part, node, arts); err != nil {
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

	// mTLS material → node agent dir.
	check(s.NodeCertPath(svc, dom, part, node), "CERT")
	check(s.NodeKeyPath(svc, dom, part, node), "KEY")
	check(s.NodeCAChainPath(svc, dom, part, node), "CHAIN")
	// CA certs → domain dir (shared).
	check(s.IdentityCAPath(svc, dom), "CHAIN")
	check(s.PermissionsCAPath(svc, dom), "CHAIN")
	// governance → domain dir (shared).
	check(s.GovernancePath(svc, dom), "GOV")

	info, err := os.Stat(s.NodeKeyPath(svc, dom, part, node))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("node.key mode: got %04o, want 0600", info.Mode().Perm())
	}
}

func TestResolveNodeMTLSAndURL(t *testing.T) {
	s := newTestStore(t)
	const (
		svc  = "edge-prov"
		dom  = "0:domain-0849"
		part = "participant-sensors-0849"
		node = "SN-001"
	)
	// Nothing stored yet: resolve is a no-op and URL is empty.
	if c, k, ca := s.ResolveNodeMTLS(svc, dom, part, node, "", "", ""); c != "" || k != "" || ca != "" {
		t.Fatalf("expected empty mTLS resolution, got %q %q %q", c, k, ca)
	}
	if u := s.ResolveNodeURL(svc, dom, part, node); u != "" {
		t.Fatalf("expected empty URL, got %q", u)
	}

	if err := s.WriteEnrollArtifacts(svc, dom, part, node, EnrollArtifacts{
		DeviceCertPEM: []byte("CERT"), PrivateKeyPEM: []byte("KEY"), CAChainPEM: []byte("CHAIN"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteNodeURL(svc, dom, part, node, "https://node.example.com"); err != nil {
		t.Fatal(err)
	}

	cert, key, ca := s.ResolveNodeMTLS(svc, dom, part, node, "", "", "")
	if cert != s.NodeCertPath(svc, dom, part, node) || key != s.NodeKeyPath(svc, dom, part, node) || ca != s.NodeCAChainPath(svc, dom, part, node) {
		t.Fatalf("unexpected mTLS resolution: %q %q %q", cert, key, ca)
	}
	// Caller-supplied values are preserved.
	if c, _, _ := s.ResolveNodeMTLS(svc, dom, part, node, "explicit", "", ""); c != "explicit" {
		t.Fatalf("explicit cert should be preserved, got %q", c)
	}
	if u := s.ResolveNodeURL(svc, dom, part, node); u != "https://node.example.com" {
		t.Fatalf("unexpected URL: %q", u)
	}
}
