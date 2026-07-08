// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wireDirectEnroll installs an EnrollDirectFunc stub that records its
// arguments and simulates the store writes EnrollDeviceDirect performs.
func wireDirectEnroll(a *Agent, ffs *fakeFS, nodeURL string, got *[]string) {
	a.EnrollDirectFunc = func(serviceID, domainTemplateID, participantTemplateID, serial string, _ []string, _, _, keyFile string) (string, string, error) {
		*got = []string{serviceID, domainTemplateID, participantTemplateID, serial}
		keyData, _ := os.ReadFile(keyFile)
		mtlsDir := a.Store.NodeAgentDir(serviceID, domainTemplateID, participantTemplateID, serial)
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "node.key"), keyData, 0o600)
		return domainTemplateID, nodeURL, nil
	}
}

func TestConfigureFirstRun_OperatorAutoSelectsSingleEntries(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}
	a.DeviceID = "SN-001"
	a.MACs = []string{"AA:BB:CC:DD:EE:01"}
	a.ListServicesFunc = func() ([]string, error) { return []string{"svc"}, nil }
	a.ListDomainTemplatesFunc = func(string) ([]string, error) { return []string{"1:dom"}, nil }
	a.ListParticipantTemplatesFunc = func(string) ([]string, error) { return []string{"part"}, nil }

	var enrolled []string
	wireDirectEnroll(a, ffs, "https://device.example", &enrolled)

	var identityURL string
	a.RequestIdentityFunc = func(url, _, _, _, _, _, output string) error {
		identityURL = url
		dir := strings.TrimSuffix(output, string(os.PathSeparator))
		ffs.WriteFile(filepath.Join(dir, "identity.crt"), []byte("FAKE-CERT"), 0o644)
		return nil
	}

	selects := 0
	a.SelectFunc = func(message string, choices []string) (string, error) {
		selects++
		if !strings.Contains(message, "How do you want to enroll") {
			t.Fatalf("unexpected select prompt: %q", message)
		}
		return enrollChoiceOperator, nil
	}
	a.InputFunc = func(message string) (string, error) {
		t.Fatalf("unexpected input prompt: %q", message)
		return "", nil
	}

	if err := a.ConfigureFirstRun(context.Background()); err != nil {
		t.Fatalf("ConfigureFirstRun: %v", err)
	}
	if selects != 1 {
		t.Fatalf("expected exactly one select (the mode question), got %d", selects)
	}
	want := []string{"svc", "1:dom", "part", "SN-001"}
	if strings.Join(enrolled, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected enrollment args: %v (want %v)", enrolled, want)
	}
	// The device endpoint URL from the enroll-node response must drive the
	// artifact fetches when the store has no stored node_url yet.
	if identityURL != "https://device.example" {
		t.Fatalf("identity fetched from %q, want the enrollment nodeUrl", identityURL)
	}
}

func TestConfigureFirstRun_OperatorPickListsMultipleEntries(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}
	a.DeviceID = "SN-002"
	a.MACs = []string{"AA:BB:CC:DD:EE:02"}
	a.ListServicesFunc = func() ([]string, error) { return []string{"svc-a", "svc-b"}, nil }
	a.ListDomainTemplatesFunc = func(service string) ([]string, error) {
		if service != "svc-b" {
			t.Fatalf("domain templates listed for %q, want svc-b", service)
		}
		return []string{"1:dom-a", "2:dom-b"}, nil
	}
	a.ListParticipantTemplatesFunc = func(string) ([]string, error) {
		return []string{"part-a", "part-b"}, nil
	}

	var enrolled []string
	wireDirectEnroll(a, ffs, "https://device.example", &enrolled)

	a.SelectFunc = func(message string, choices []string) (string, error) {
		switch {
		case strings.Contains(message, "How do you want to enroll"):
			return enrollChoiceOperator, nil
		case strings.Contains(message, "Select Provisioning Service"):
			return "svc-b", nil
		case strings.Contains(message, "Select Domain Template"):
			return "2:dom-b", nil
		case strings.Contains(message, "Select Participant Template"):
			return "part-b", nil
		}
		t.Fatalf("unexpected select prompt: %q", message)
		return "", nil
	}

	if err := a.ConfigureFirstRun(context.Background()); err != nil {
		t.Fatalf("ConfigureFirstRun: %v", err)
	}
	want := []string{"svc-b", "2:dom-b", "part-b", "SN-002"}
	if strings.Join(enrolled, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected enrollment args: %v (want %v)", enrolled, want)
	}
}

func TestConfigureFirstRun_OperatorEmptyCatalogueRetriesThenExits(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.DeviceID = "SN-003"
	listCalls := 0
	a.ListServicesFunc = func() ([]string, error) { listCalls++; return nil, nil }
	a.ListDomainTemplatesFunc = func(string) ([]string, error) { return nil, nil }
	a.ListParticipantTemplatesFunc = func(string) ([]string, error) { return nil, nil }

	a.SelectFunc = func(message string, choices []string) (string, error) {
		switch {
		case strings.Contains(message, "How do you want to enroll"):
			return enrollChoiceOperator, nil
		case strings.Contains(message, "What would you like to do?"):
			if listCalls < 2 {
				return "try again", nil
			}
			return "exit", nil
		}
		t.Fatalf("unexpected select prompt: %q", message)
		return "", nil
	}

	err := a.ConfigureFirstRun(context.Background())
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if listCalls != 2 {
		t.Fatalf("expected 2 catalogue attempts (initial + retry), got %d", listCalls)
	}
}

func TestConfigureFirstRun_HeadlessDirect(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}
	a.Service = "svc"
	a.DomainTemplateID = "1:dom"
	a.ParticipantTemplateID = "part"
	a.DeviceID = "SN-004"

	var enrolled []string
	wireDirectEnroll(a, ffs, "https://device.example", &enrolled)

	a.SelectFunc = func(message string, choices []string) (string, error) {
		t.Fatalf("prompted in headless mode: %q", message)
		return "", nil
	}
	a.InputFunc = func(message string) (string, error) {
		t.Fatalf("prompted in headless mode: %q", message)
		return "", nil
	}

	if err := a.ConfigureFirstRun(context.Background()); err != nil {
		t.Fatalf("ConfigureFirstRun: %v", err)
	}
	want := []string{"svc", "1:dom", "part", "SN-004"}
	if strings.Join(enrolled, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected enrollment args: %v (want %v)", enrolled, want)
	}
}

func TestConfigureFirstRun_HeadlessCampaign(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}
	a.CampaignToken = buildJWT(map[string]any{
		"edge_system_id": "svc",
		"participant_id": "part",
		"device_domain":  "svc.devices.example",
	})
	a.DeviceID = "SN-005"
	a.MACs = []string{"AA:BB:CC:DD:EE:05"}

	var enrolledToken string
	a.EnrollFunc = func(serviceID, participantID, serial string, _ []string, _, keyFile, campaignToken string) (string, error) {
		enrolledToken = campaignToken
		keyData, _ := os.ReadFile(keyFile)
		mtlsDir := a.Store.NodeAgentDir(serviceID, "dom", participantID, serial)
		ffs.MkdirAll(mtlsDir, 0o755)
		ffs.WriteFile(filepath.Join(mtlsDir, "node.key"), keyData, 0o600)
		return "dom", nil
	}

	a.SelectFunc = func(message string, choices []string) (string, error) {
		t.Fatalf("prompted in headless mode: %q", message)
		return "", nil
	}

	if err := a.ConfigureFirstRun(context.Background()); err != nil {
		t.Fatalf("ConfigureFirstRun: %v", err)
	}
	if enrolledToken != a.CampaignToken {
		t.Fatal("campaign token not passed to EnrollFunc")
	}
}

func TestConfigureFirstRun_CampaignModeViaWizard(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}
	a.DeviceID = "SN-006"
	a.MACs = []string{"AA:BB:CC:DD:EE:06"}

	token := buildJWT(map[string]any{
		"edge_system_id": "svc",
		"participant_id": "part",
		"device_domain":  "svc.devices.example",
	})
	a.In = strings.NewReader(token + "\n")

	a.SelectFunc = func(message string, choices []string) (string, error) {
		if !strings.Contains(message, "How do you want to enroll") {
			t.Fatalf("unexpected select prompt: %q", message)
		}
		return enrollChoiceCampaign, nil
	}

	if err := a.ConfigureFirstRun(context.Background()); err != nil {
		t.Fatalf("ConfigureFirstRun: %v", err)
	}
	if _, ok := a.profiles.Load(profileKey("dom", "part", "")); !ok {
		t.Fatal("campaign profile not stored after wizard enrollment")
	}
}

func TestDrainInbox_ProcessesDirectRequest(t *testing.T) {
	ffs := newFakeFS()
	a := buildTestAgent(t, ffs)
	a.AfterFunc = func(d time.Duration, f func()) *time.Timer {
		return time.AfterFunc(10*time.Hour, f)
	}

	var enrolled []string
	wireDirectEnroll(a, ffs, "https://device.example", &enrolled)

	// Direct requests carry a domain template instead of a campaign token and
	// may omit MACs.
	req := EnrollRequest{
		ServiceID:        "svc",
		ParticipantID:    "part",
		DomainTemplateID: "1:dom",
		Serial:           "SN-007",
	}
	data, _ := json.Marshal(req)
	inboxFile := "/connext/agent/inbox/enroll-direct.json"
	ffs.WriteFile(inboxFile, data, 0o644)

	a.drainInbox()

	if len(enrolled) == 0 {
		t.Fatal("direct inbox request did not reach EnrollDirectFunc")
	}
	want := []string{"svc", "1:dom", "part", "SN-007"}
	if strings.Join(enrolled, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected enrollment args: %v (want %v)", enrolled, want)
	}
	if !contains(ffs.removed, inboxFile) {
		t.Fatalf("processed request not removed from inbox; removed: %v", ffs.removed)
	}
}
