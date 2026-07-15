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
// WriteFile/MkdirAll/Stat are injectable for testing.
type Store struct {
	// BaseDir is the agent base directory (typically <workdir>/.connext). It
	// always holds the agent's operational files (inbox, log, mTLS credentials
	// and per-node state) regardless of ConnextDir.
	BaseDir string

	// ConnextDir, when non-empty (set via --connext-dir), relocates the
	// connext_artifacts <service> root to an arbitrary directory. The agent
	// base (BaseDir) is unaffected. When empty, the connext artifacts default
	// to BaseDir/agent/connext_artifacts/<service>.
	ConnextDir string

	WriteFile func(path string, data []byte, perm os.FileMode) error
	MkdirAll  func(path string, perm os.FileMode) error
	Stat      func(path string) (os.FileInfo, error)
}

// EnrollArtifacts holds the security material returned by a successful enrollment.
type EnrollArtifacts struct {
	DeviceCertPEM []byte // written to mtls_artifacts/node.crt  (0644)
	CAChainPEM    []byte // written to mtls_artifacts/ca-chain.crt (0644) and, as identity_ca.crt + permissions_ca.crt, to connext_artifacts/<domain>/ (0644)
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

func (s *Store) fileExists(path string) bool {
	_, err := s.Stat(path)
	return err == nil
}

// ─── Layered artifact layout ─────────────────────────────────────────────────
//
// Artifacts are grouped by the scope at which they are shared:
//
//	<BaseDir>/agent/
//	  inbox/
//	  rticloud-edge-agent.log
//	  <service>/
//	    connext_artifacts/
//	      <domain>/                                     DOMAIN: identity_ca.crt, permissions_ca.crt,
//	                                                            crl.pem, governance.p7s, psk_*
//	        <participant>/<node>/                       NODE:   identity.crt, permissions.p7s, leases
//	    mtls_artifacts/
//	      <domain>/<participant>/<node>/                NODE:   node.crt, node.key, ca-chain.crt, node_url,
//	                                                            agent_state.json
//
// When ConnextDir is set, the connext_artifacts <service> root is replaced by
// ConnextDir (the <service> level collapses into the supplied directory); the
// agent base — inbox, log, mtls_artifacts and per-node state — stays under
// BaseDir.
//
// The identifiers used throughout are:
//
//	service     — provisioning service id      (e.g. edge-provisioning-greenfield)
//	domain      — domain template id           (e.g. 0:domain-0849)
//	participant — participant template id      (e.g. participant-sensors-0849)
//	node        — node id (device serial)      (e.g. b9a00ae9a51d4086b52dc96015e4c5b0)

// AgentDir returns the agent base directory that holds the agent's operational
// files (inbox, log, mTLS credentials, per-node state). It is never relocated
// by ConnextDir.
func (s *Store) AgentDir() string {
	return filepath.Join(s.BaseDir, "agent")
}

// InboxDir returns the directory watched for enroll-*.json requests.
func (s *Store) InboxDir() string {
	return filepath.Join(s.AgentDir(), "inbox")
}

// LogPath returns the default agent log file path.
func (s *Store) LogPath() string {
	return filepath.Join(s.AgentDir(), "rticloud-edge-agent.log")
}

// ServiceDir returns the SERVICE-scope root that holds the connext artifacts
// tree for the provisioning service. It is the directory relocated by
// ConnextDir.
func (s *Store) ServiceDir(service string) string {
	if s.ConnextDir != "" {
		return s.ConnextDir
	}
	return filepath.Join(s.AgentDir(), service, "connext_artifacts")
}

// IdentityCAPath is the DDS identity CA, shared by every participant template
// in the domain.
func (s *Store) IdentityCAPath(service, domain string) string {
	return filepath.Join(s.DomainDir(service, domain), "identity_ca.crt")
}

// PermissionsCAPath is the DDS permissions/governance CA, shared by every
// participant template in the domain.
func (s *Store) PermissionsCAPath(service, domain string) string {
	return filepath.Join(s.DomainDir(service, domain), "permissions_ca.crt")
}

// DomainDir returns the DOMAIN-scope directory that holds artifacts shared by
// every participant template in the domain (governance, CRL, PSK).
func (s *Store) DomainDir(service, domain string) string {
	return filepath.Join(s.ServiceDir(service), domain)
}

// GovernancePath is the signed governance document, shared across the domain.
func (s *Store) GovernancePath(service, domain string) string {
	return filepath.Join(s.DomainDir(service, domain), "governance.p7s")
}

// CRLPath is the certificate revocation list, shared across the domain.
func (s *Store) CRLPath(service, domain string) string {
	return filepath.Join(s.DomainDir(service, domain), "crl.pem")
}

// NodeDir returns the NODE-scope directory that holds the participant-specific
// DDS identity and permissions material.
func (s *Store) NodeDir(service, domain, participant, node string) string {
	return filepath.Join(s.DomainDir(service, domain), participant, node)
}

// IdentityCertPath is the DDS identity certificate for a node.
func (s *Store) IdentityCertPath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeDir(service, domain, participant, node), "identity.crt")
}

// IdentityKeyPath is the dedicated DDS identity private key for a node (kept
// separate from the mTLS device key).
func (s *Store) IdentityKeyPath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeDir(service, domain, participant, node), "identity.key")
}

// IdentityLeasePath is the DDS identity lease window for a node.
func (s *Store) IdentityLeasePath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeDir(service, domain, participant, node), "identity.lease.json")
}

// PermissionsPath is the signed permissions document for a node.
func (s *Store) PermissionsPath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeDir(service, domain, participant, node), "permissions.p7s")
}

// PermissionsLeasePath is the permissions lease window for a node.
func (s *Store) PermissionsLeasePath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeDir(service, domain, participant, node), "permissions.lease.json")
}

// MTLSRoot returns the root of the per-node mTLS/agent-state tree for the
// given service. It always lives under BaseDir and is never relocated by
// ConnextDir.
func (s *Store) MTLSRoot(service string) string {
	return filepath.Join(s.AgentDir(), service, "mtls_artifacts")
}

// NodeAgentDir returns the per-node agent directory under mtls_artifacts that
// holds the node's transport credentials and operational state. It mirrors the
// connext_artifacts node path but always lives under BaseDir (never relocated
// by ConnextDir).
func (s *Store) NodeAgentDir(service, domain, participant, node string) string {
	return filepath.Join(s.MTLSRoot(service), domain, participant, node)
}

// NodeCertPath is the mTLS leaf certificate for a node.
func (s *Store) NodeCertPath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeAgentDir(service, domain, participant, node), "node.crt")
}

// NodeKeyPath is the mTLS private key for a node.
func (s *Store) NodeKeyPath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeAgentDir(service, domain, participant, node), "node.key")
}

// NodeCAChainPath is the Provisioning Service CA chain (mTLS transport trust)
// for a node.
func (s *Store) NodeCAChainPath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeAgentDir(service, domain, participant, node), "ca-chain.crt")
}

// NodeURLPath is the stored device endpoint URL for a node.
func (s *Store) NodeURLPath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeAgentDir(service, domain, participant, node), "node_url")
}

// NodeStatePath is the persisted agent state for a node.
func (s *Store) NodeStatePath(service, domain, participant, node string) string {
	return filepath.Join(s.NodeAgentDir(service, domain, participant, node), "agent_state.json")
}

// WriteEnrollArtifacts persists enrollment artifacts into the layered layout:
//
//	node.crt / node.key / ca-chain.crt   → NodeAgentDir (mTLS, per node)
//	identity_ca.crt + permissions_ca.crt → DomainDir    (shared by the domain)
//	governance.p7s                      → DomainDir    (shared by the domain)
//
// Fields with nil or empty bytes are silently skipped.
func (s *Store) WriteEnrollArtifacts(service, domain, participant, node string, a EnrollArtifacts) error {
	if err := s.MkdirAll(s.NodeAgentDir(service, domain, participant, node), 0o755); err != nil {
		return err
	}
	if len(a.DeviceCertPEM) > 0 {
		if err := s.WriteFile(s.NodeCertPath(service, domain, participant, node), a.DeviceCertPEM, 0o644); err != nil {
			return err
		}
	}
	if len(a.PrivateKeyPEM) > 0 {
		if err := s.WriteFile(s.NodeKeyPath(service, domain, participant, node), a.PrivateKeyPEM, 0o600); err != nil {
			return err
		}
	}
	if len(a.CAChainPEM) > 0 {
		if err := s.WriteFile(s.NodeCAChainPath(service, domain, participant, node), a.CAChainPEM, 0o644); err != nil {
			return err
		}
		if err := s.MkdirAll(s.DomainDir(service, domain), 0o755); err != nil {
			return err
		}
		if err := s.WriteFile(s.IdentityCAPath(service, domain), a.CAChainPEM, 0o644); err != nil {
			return err
		}
		if err := s.WriteFile(s.PermissionsCAPath(service, domain), a.CAChainPEM, 0o644); err != nil {
			return err
		}
	}
	if len(a.GovernanceP7S) > 0 {
		if err := s.MkdirAll(s.DomainDir(service, domain), 0o755); err != nil {
			return err
		}
		if err := s.WriteFile(s.GovernancePath(service, domain), a.GovernanceP7S, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// ResolveNodeMTLS fills cert, key, and ca from the node's mTLS directory when
// the caller did not supply them and the stored files exist on disk.
func (s *Store) ResolveNodeMTLS(service, domain, participant, node, cert, key, ca string) (string, string, string) {
	if cert == "" {
		if p := s.NodeCertPath(service, domain, participant, node); s.fileExists(p) {
			cert = p
		}
	}
	if key == "" {
		if p := s.NodeKeyPath(service, domain, participant, node); s.fileExists(p) {
			key = p
		}
	}
	if ca == "" {
		if p := s.NodeCAChainPath(service, domain, participant, node); s.fileExists(p) {
			ca = p
		}
	}
	return cert, key, ca
}

// WriteNodeURL persists the device endpoint URL for a node, creating the node
// directory as needed. An empty url is silently ignored.
func (s *Store) WriteNodeURL(service, domain, participant, node, url string) error {
	if url == "" {
		return nil
	}
	if err := s.MkdirAll(s.NodeAgentDir(service, domain, participant, node), 0o755); err != nil {
		return err
	}
	return s.WriteFile(s.NodeURLPath(service, domain, participant, node), []byte(url), 0o644)
}

// ResolveNodeURL returns the stored device endpoint URL for a node, or "" if
// none has been saved yet.
func (s *Store) ResolveNodeURL(service, domain, participant, node string) string {
	p := s.NodeURLPath(service, domain, participant, node)
	if !s.fileExists(p) {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// NodeInfo describes an enrolled node that has a stored node_url.
type NodeInfo struct {
	Service     string
	Domain      string
	Participant string
	Node        string
	URL         string
	EnrolledAt  time.Time // mtime of the node_url file; zero if unknown
}

// ListNodesWithURL walks the agent mTLS tree
// (<service>/mtls_artifacts/<domain>/<participant>/<node>) and returns every
// node that has a stored node_url. Returns nil when the tree does not exist or
// cannot be read.
func (s *Store) ListNodesWithURL() []NodeInfo {
	agentRoot := s.AgentDir()
	var nodes []NodeInfo
	services, err := os.ReadDir(agentRoot)
	if err != nil {
		return nil
	}
	for _, svc := range services {
		if !svc.IsDir() || svc.Name() == "inbox" {
			continue
		}
		mtlsRoot := s.MTLSRoot(svc.Name())
		domains, err := os.ReadDir(mtlsRoot)
		if err != nil {
			continue
		}
		for _, dom := range domains {
			if !dom.IsDir() {
				continue
			}
			parts, err := os.ReadDir(filepath.Join(mtlsRoot, dom.Name()))
			if err != nil {
				continue
			}
			for _, part := range parts {
				if !part.IsDir() {
					continue
				}
				leaves, err := os.ReadDir(filepath.Join(mtlsRoot, dom.Name(), part.Name()))
				if err != nil {
					continue
				}
				for _, leaf := range leaves {
					if !leaf.IsDir() {
						continue
					}
					urlPath := s.NodeURLPath(svc.Name(), dom.Name(), part.Name(), leaf.Name())
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
					nodes = append(nodes, NodeInfo{
						Service:     svc.Name(),
						Domain:      dom.Name(),
						Participant: part.Name(),
						Node:        leaf.Name(),
						URL:         u,
						EnrolledAt:  fi.ModTime(),
					})
				}
			}
		}
	}
	return nodes
}
