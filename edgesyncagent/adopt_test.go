// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"context"
	"strings"
	"testing"
	"time"
)

// seedOrphanEnrollment writes the on-disk footprint left by a standalone
// `rticloud edge-provisioning enroll`/`enroll-direct`: mTLS credentials and a
// stored device endpoint URL, but no agent_state.json.
func seedOrphanEnrollment(a *Agent, ffs *fakeFS, service, domain, participant, node, url string) {
	ffs.WriteFile(a.Store.NodeKeyPath(service, domain, participant, node), []byte("KEY"), 0o600)
	ffs.WriteFile(a.Store.NodeURLPath(service, domain, participant, node), []byte(url), 0o644)
}

// nonFiringTimers wires AfterFunc so scheduled renewals never fire during the
// test.
func nonFiringTimers(a *Agent) {
	a.AfterFunc = func(_ time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}
}

func TestFindAdoptableNodes_DetectsOrphansOnly(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	// (1) A genuine orphan: node.key + node_url, no agent_state.json.
	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part", "SN-orphan", "https://device.example")

	// (2) A node with credentials but no node_url — not adoptable.
	ffs.WriteFile(a.Store.NodeKeyPath("svc", "0:dom", "part", "SN-nourl"), []byte("KEY"), 0o600)

	// (3) A managed node that already has agent_state.json — not adoptable.
	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part", "SN-managed", "https://device.example")
	ffs.WriteFile(a.Store.NodeStatePath("svc", "0:dom", "part", "SN-managed"), []byte("{}"), 0o644)

	got := a.findAdoptableNodes()
	if len(got) != 1 {
		t.Fatalf("expected exactly one adoptable node, got %d: %+v", len(got), got)
	}
	n := got[0]
	if n.service != "svc" || n.domain != "0:dom" || n.participant != "part" ||
		n.node != "SN-orphan" || n.url != "https://device.example" {
		t.Fatalf("unexpected adoptable node: %+v", n)
	}
}

func TestFindAdoptableNodes_ExcludesInMemoryProfile(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part", "SN-orphan", "https://device.example")
	// A profile already keyed to the same node slot means it is being managed;
	// it must not be reported as adoptable.
	a.profiles.Store(profileKey("0:dom", "part", "SN-orphan"), &profile{
		serviceID:        "svc",
		domainTemplateID: "0:dom",
		participantID:    "part",
		serial:           "SN-orphan",
	})

	if got := a.findAdoptableNodes(); len(got) != 0 {
		t.Fatalf("expected no adoptable nodes when a profile exists, got %+v", got)
	}
}

func TestAdoptProfile_ActivatesFromExistingCredentials(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	nonFiringTimers(a)
	a.Now = func() time.Time { return time.Unix(0, 0) }

	// Enrollment must never be attempted when adopting.
	a.EnrollFunc = func(string, string, string, []string, string, string, string) (string, error) {
		t.Fatal("EnrollFunc called during adoption")
		return "", nil
	}
	a.EnrollDirectFunc = func(string, string, string, string, []string, string, string, string) (string, string, error) {
		t.Fatal("EnrollDirectFunc called during adoption")
		return "", "", nil
	}

	var identityURL string
	a.RequestIdentityFunc = func(url, _, _, _, _, _, output string) error {
		identityURL = url
		dir := strings.TrimSuffix(output, "/")
		ffs.WriteFile(dir+"/identity.crt", []byte("FAKE-CERT"), 0o644)
		return nil
	}

	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part", "SN-orphan", "https://device.example")

	nodes := a.findAdoptableNodes()
	if len(nodes) != 1 {
		t.Fatalf("expected one adoptable node, got %d", len(nodes))
	}
	if err := a.adoptProfile(nodes[0]); err != nil {
		t.Fatalf("adoptProfile: %v", err)
	}

	// The artifact fetch must have used the stored node_url.
	if identityURL != "https://device.example" {
		t.Fatalf("identity fetched from %q, want the stored node_url", identityURL)
	}

	val, ok := a.profiles.Load(profileKey("0:dom", "part", "SN-orphan"))
	if !ok {
		t.Fatal("adopted profile not stored")
	}
	p := val.(*profile)
	if p.state != StateActive {
		t.Fatalf("adopted profile state = %s, want %s", p.state, StateActive)
	}
	if p.serviceID != "svc" || p.domainTemplateID != "0:dom" ||
		p.participantID != "part" || p.serial != "SN-orphan" {
		t.Fatalf("adopted profile has wrong identifiers: %+v", p)
	}
	// agent_state.json must now exist so a later run rehydrates instead of
	// re-adopting.
	if _, err := ffs.ReadFile(a.Store.NodeStatePath("svc", "0:dom", "part", "SN-orphan")); err != nil {
		t.Fatalf("agent_state.json not persisted after adoption: %v", err)
	}
	// The node is no longer adoptable once managed.
	if got := a.findAdoptableNodes(); len(got) != 0 {
		t.Fatalf("node still adoptable after adoption: %+v", got)
	}
}

func TestAdoptProfile_FailureDropsProfile(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	nonFiringTimers(a)

	// A node without a stored node_url still reaches adoptProfile only if we
	// forge the descriptor; force the fetch to fail by returning no URL and an
	// identity error so the half-built profile is dropped.
	a.RequestIdentityFunc = func(string, string, string, string, string, string, string) error {
		return context.DeadlineExceeded
	}
	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part", "SN-orphan", "https://device.example")

	n := adoptableNode{service: "svc", domain: "0:dom", participant: "part", node: "SN-orphan", url: "https://device.example"}
	if err := a.adoptProfile(n); err == nil {
		t.Fatal("expected adoptProfile to fail")
	}
	if _, ok := a.profiles.Load(profileKey("0:dom", "part", "SN-orphan")); ok {
		t.Fatal("failed adoption left a profile behind")
	}
}

func TestAdoptExistingEnrollments_SilentlyAdoptsAll(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	nonFiringTimers(a)
	a.Now = func() time.Time { return time.Unix(0, 0) }

	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part-a", "SN-a", "https://a.example")
	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part-b", "SN-b", "https://b.example")

	a.adoptExistingEnrollments()

	for part, serial := range map[string]string{"part-a": "SN-a", "part-b": "SN-b"} {
		if _, ok := a.profiles.Load(profileKey("0:dom", part, serial)); !ok {
			t.Fatalf("orphan %s not adopted", part)
		}
	}
}

// TestAdopt_SameParticipantDifferentSerials reproduces the s-tractor fleet case:
// two nodes enrolled from the SAME participant template ("tractor") in the same
// domain, differing only by serial. Both must be detected and adopted into
// distinct profiles — the earlier bug keyed them identically, so only one
// survived.
func TestAdopt_SameParticipantDifferentSerials(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	nonFiringTimers(a)
	a.Now = func() time.Time { return time.Unix(0, 0) }

	const domain, participant = "29:south-field", "tractor"
	seedOrphanEnrollment(a, ffs, "svc", domain, participant, "Fendt-724-010", "https://a.example")
	seedOrphanEnrollment(a, ffs, "svc", domain, participant, "Fendt-728-011", "https://b.example")

	adoptable := a.findAdoptableNodes()
	if len(adoptable) != 2 {
		t.Fatalf("expected 2 adoptable nodes sharing a participant, got %d", len(adoptable))
	}

	a.adoptExistingEnrollments()

	for _, serial := range []string{"Fendt-724-010", "Fendt-728-011"} {
		if _, ok := a.profiles.Load(profileKey(domain, participant, serial)); !ok {
			t.Fatalf("node %s (same participant) not adopted into its own profile", serial)
		}
	}
	if n := a.countProfiles(); n != 2 {
		t.Fatalf("expected 2 distinct profiles, got %d", n)
	}
}

func TestConfigureFirstRun_ReuseChoiceAdoptsExisting(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	nonFiringTimers(a)
	a.Now = func() time.Time { return time.Unix(0, 0) }

	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part", "SN-orphan", "https://device.example")

	a.EnrollFunc = func(string, string, string, []string, string, string, string) (string, error) {
		t.Fatal("EnrollFunc called when reusing an enrollment")
		return "", nil
	}
	a.EnrollDirectFunc = func(string, string, string, string, []string, string, string, string) (string, string, error) {
		t.Fatal("EnrollDirectFunc called when reusing an enrollment")
		return "", "", nil
	}

	var offered []string
	a.SelectFunc = func(message string, choices []string) (string, error) {
		if !strings.Contains(message, "How do you want to enroll") {
			t.Fatalf("unexpected select prompt: %q", message)
		}
		offered = choices
		return enrollChoiceReuse, nil
	}
	a.InputFunc = func(message string) (string, error) {
		t.Fatalf("unexpected input prompt: %q", message)
		return "", nil
	}

	if err := a.ConfigureFirstRun(context.Background()); err != nil {
		t.Fatalf("ConfigureFirstRun: %v", err)
	}
	if offered[0] != enrollChoiceReuse {
		t.Fatalf("reuse choice not offered first: %v", offered)
	}
	if _, ok := a.profiles.Load(profileKey("0:dom", "part", "SN-orphan")); !ok {
		t.Fatal("reused enrollment not adopted into a profile")
	}
}

func TestConfigureFirstRun_NoReuseChoiceWithoutOrphans(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	nonFiringTimers(a)
	a.DeviceID = "SN-001"
	a.MACs = []string{"AA:BB:CC:DD:EE:01"}
	a.ListServicesFunc = func() ([]string, error) { return []string{"svc"}, nil }
	a.ListDomainTemplatesFunc = func(string) ([]string, error) { return []string{"1:dom"}, nil }
	a.ListParticipantTemplatesFunc = func(string) ([]string, error) { return []string{"part"}, nil }

	var enrolled []string
	wireDirectEnroll(a, ffs, "https://device.example", &enrolled)

	a.SelectFunc = func(message string, choices []string) (string, error) {
		if strings.Contains(message, "How do you want to enroll") {
			for _, c := range choices {
				if c == enrollChoiceReuse {
					t.Fatalf("reuse choice offered with no orphan enrollments: %v", choices)
				}
			}
			return enrollChoiceOperator, nil
		}
		t.Fatalf("unexpected select prompt: %q", message)
		return "", nil
	}

	if err := a.ConfigureFirstRun(context.Background()); err != nil {
		t.Fatalf("ConfigureFirstRun: %v", err)
	}
	if len(enrolled) == 0 {
		t.Fatal("operator enrollment did not run")
	}
}

func TestChooseAdoptable_MultipleOffersAllThenIndividuals(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	nodes := []adoptableNode{
		{service: "svc", domain: "0:dom", participant: "part-a", node: "SN-a", url: "https://a"},
		{service: "svc", domain: "0:dom", participant: "part-b", node: "SN-b", url: "https://b"},
	}

	var offered []string
	a.SelectFunc = func(message string, choices []string) (string, error) {
		if !strings.Contains(message, "Select the enrollment(s) to reuse") {
			t.Fatalf("unexpected select prompt: %q", message)
		}
		offered = choices
		// Pick an individual enrollment (the last one).
		return choices[len(choices)-1], nil
	}

	chosen, err := a.chooseAdoptable(nodes)
	if err != nil {
		t.Fatalf("chooseAdoptable: %v", err)
	}
	// The pick-list must offer an "all" entry first, then each enrollment.
	if len(offered) != 3 {
		t.Fatalf("expected 3 choices (all + 2), got %d: %v", len(offered), offered)
	}
	if !strings.Contains(offered[0], "Use all 2 detected enrollments") {
		t.Fatalf("first choice should adopt all, got %q", offered[0])
	}
	if len(chosen) != 1 || chosen[0].node != "SN-b" {
		t.Fatalf("chose %+v, want single SN-b", chosen)
	}
}

func TestChooseAdoptable_AllSelectsEveryNode(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)

	nodes := []adoptableNode{
		{service: "svc", domain: "0:dom", participant: "part-a", node: "SN-a", url: "https://a"},
		{service: "svc", domain: "0:dom", participant: "part-b", node: "SN-b", url: "https://b"},
	}

	a.SelectFunc = func(_ string, choices []string) (string, error) {
		return choices[0], nil // "Use all ..."
	}

	chosen, err := a.chooseAdoptable(nodes)
	if err != nil {
		t.Fatalf("chooseAdoptable: %v", err)
	}
	if len(chosen) != 2 {
		t.Fatalf("expected all 2 nodes, got %d", len(chosen))
	}
}

func TestReuseWizard_AdoptsAllSelectedEnrollments(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	nonFiringTimers(a)
	a.Now = func() time.Time { return time.Unix(0, 0) }

	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part-a", "SN-a", "https://a.example")
	seedOrphanEnrollment(a, ffs, "svc", "0:dom", "part-b", "SN-b", "https://b.example")

	a.EnrollFunc = func(string, string, string, []string, string, string, string) (string, error) {
		t.Fatal("EnrollFunc called when reusing enrollments")
		return "", nil
	}

	adoptable := a.findAdoptableNodes()
	if len(adoptable) != 2 {
		t.Fatalf("expected 2 adoptable nodes, got %d", len(adoptable))
	}

	a.SelectFunc = func(_ string, choices []string) (string, error) {
		return choices[0], nil // "Use all 2 detected enrollments"
	}

	if err := a.reuseWizard(context.Background(), adoptable); err != nil {
		t.Fatalf("reuseWizard: %v", err)
	}
	for part, serial := range map[string]string{"part-a": "SN-a", "part-b": "SN-b"} {
		if _, ok := a.profiles.Load(profileKey("0:dom", part, serial)); !ok {
			t.Fatalf("enrollment %s not adopted", part)
		}
	}
}
