// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/cloudapi"
	"github.com/realtimeinnovations/connext-cloud-cli/commands"
	"github.com/realtimeinnovations/connext-cloud-cli/edgesyncagent"
)

func TestAgentEnrollWritesInboxRequest(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	agent := &edgesyncagent.Agent{InboxDir: inbox}
	rt := inboxTestRuntime(agent)
	err := Execute([]string{"edge-sync", "agent", "enroll", "--service", "svc/team", "--domain-tpl-id", "0:domain", "--participant-tpl-id", "part", "--deployment-name", "device", "--mac", "00:11:22:33:44:55", "--non-interactive"}, io.Discard, io.Discard, rt)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(inbox, "enroll-svc_team-part.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request edgesyncagent.EnrollRequest
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if request.ServiceID != "svc/team" || request.ParticipantID != "part" || request.DomainTemplateID != "0:domain" || request.Serial != "device" {
		t.Fatalf("unexpected enrollment request: %+v", request)
	}
}

func TestAgentEnrollRejectsWindowsInboxEscape(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("backslash traversal requires Windows path semantics")
	}
	for _, existing := range []bool{false, true} {
		name := "new parent"
		if existing {
			name = "existing file"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(root, "outside-part.json")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			service := `svc\..\..\newdir\outside`
			if existing {
				service = `svc\..\..\outside`
			}
			agent := &edgesyncagent.Agent{InboxDir: filepath.Join(root, "inbox")}
			rt := inboxTestRuntime(agent)
			err := Execute([]string{"edge-sync", "agent", "enroll", "--service", service, "--domain-tpl-id", "0:domain", "--participant-tpl-id", "part", "--deployment-name", "device", "--mac", "00:11:22:33:44:55", "--non-interactive"}, io.Discard, io.Discard, rt)
			if err == nil || !strings.Contains(err.Error(), "outside root") {
				t.Fatalf("expected containment rejection, got %v", err)
			}
			data, err := os.ReadFile(outside)
			if err != nil || string(data) != "untouched" {
				t.Fatalf("outside contents=%q, error=%v", data, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("rejected request created directories: %v, %v", entries, err)
			}
		})
	}
}

func inboxTestRuntime(agent *edgesyncagent.Agent) *app.Runtime {
	api := cloudapi.New(func() (string, error) { return "https://api.example.test", nil },
		func() (map[string]string, error) { return map[string]string{}, nil })
	api.HTTPClient = roundTripClient(func(*http.Request) (*http.Response, error) {
		return stringResponse(http.StatusOK, `{"domain_templates":[{"templateId":"0:domain","securityMode":"full"}]}`), nil
	})
	return &app.Runtime{Commands: commands.New(api, io.Discard), EdgeSyncAgent: agent}
}
