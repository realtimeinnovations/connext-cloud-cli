package diagnostics

import "testing"

func TestDetectorRecognizesAndDeduplicatesUDPUnicastSocketCreateFailure(t *testing.T) {
	detector := NewDetector()
	line := "ERROR " + UDPUnicastSocketCreateFailure + " (errno = 48)"

	finding, first := detector.Observe("routing", line)
	if !first || finding.Severity != SeverityError || finding.Summary != "A required UDP socket could not be created." || finding.Count != 1 {
		t.Fatalf("unexpected first finding: %#v, first=%v", finding, first)
	}
	finding, first = detector.Observe("routing", line)
	if first || finding.Count != 2 || len(detector.Findings()) != 1 {
		t.Fatalf("expected one deduplicated finding with count 2: %#v, first=%v", detector.Findings(), first)
	}
}

func TestDetectorDoesNotFlagPrecedingBindWarnings(t *testing.T) {
	detector := NewDetector()
	warnings := []string{
		"WARNING NDDS_Transport_UDPv4_Socket_bind_with_ip:FAILED TO BIND | Port 7780 in use",
		"WARNING NDDS_Transport_UDPv4_SocketFactory_create_send_socket:FAILED TO BIND | Invalid port 7780",
	}
	for _, line := range warnings {
		if finding, matched := detector.Observe("routing", line); matched || finding.ID != "" {
			t.Fatalf("bind warning must not create a finding: %#v", finding)
		}
	}
	if len(detector.Findings()) != 0 {
		t.Fatalf("bind warnings created findings: %#v", detector.Findings())
	}
}

func TestDetectorSummarizesDistinctComponentNotInstalledMessages(t *testing.T) {
	detector := NewDetector()
	wan := ComponentNotInstalledPrefix + "Real-Time WAN Transport, or it is not properly enabled. See https://community.rti.com/documentation for information about using and enabling Real-Time WAN Transport. To purchase it, please contact your RTI sales representative or sales@rti.com."
	security := ComponentNotInstalledPrefix + "the Security Plugins, or it is not properly enabled. See https://community.rti.com/documentation for information about using and enabling the Security Plugins. To purchase it, please contact your RTI sales representative or sales@rti.com."
	wanSummary := ComponentNotInstalledPrefix + "Real-Time WAN Transport, or it is not properly enabled."
	securitySummary := ComponentNotInstalledPrefix + "the Security Plugins, or it is not properly enabled."

	finding, first := detector.Observe("routing", "ERROR [context] FAILED TO ENABLE | "+wan+" trailing context")
	if !first || finding.Summary != wanSummary || finding.Hint != componentNotInstalledHint {
		t.Fatalf("unexpected WAN finding: %#v, first=%v", finding, first)
	}
	detector.Observe("routing", "ERROR [context] FAILED TO ENABLE | "+wan)
	finding, first = detector.Observe("routing", "ERROR [context] FAILED TO LOAD | "+security)
	if !first || finding.Summary != securitySummary || finding.Hint != componentNotInstalledHint {
		t.Fatalf("unexpected Security finding: %#v, first=%v", finding, first)
	}

	findings := detector.Findings()
	if len(findings) != 2 || findings[0].Count != 2 || findings[1].Count != 1 {
		t.Fatalf("expected distinct deduplicated component findings: %#v", findings)
	}
}
