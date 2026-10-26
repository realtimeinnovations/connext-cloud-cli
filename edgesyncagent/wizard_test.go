// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgesyncagent

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadTokenLine(t *testing.T) {
	for _, tt := range []struct {
		name        string
		input       string
		interactive bool
		want        string
		wantErr     error
	}{
		{name: "LF", input: "  token  \n", want: "token"},
		{name: "CR", input: "token\r", want: "token"},
		{name: "CRLF", input: "token\r\n", want: "token"},
		{name: "final partial line", input: "token", want: "token"},
		{name: "empty EOF", wantErr: io.EOF},
		{name: "empty line", input: "\n"},
		{name: "long token", input: strings.Repeat("x", 16000) + "\n", want: strings.Repeat("x", 16000)},
		{name: "backspace", input: "tokex\bn\r", interactive: true, want: "token"},
		{name: "delete", input: "tokex\x7fn\r", interactive: true, want: "token"},
		{name: "delete empty", input: "\x7ftoken\r", interactive: true, want: "token"},
		{name: "clear line", input: "wrong\x15token\r", interactive: true, want: "token"},
		{name: "Ctrl+D", input: "token\x04", interactive: true, wantErr: io.EOF},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readTokenLine(strings.NewReader(tt.input), tt.interactive)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %q, %v; want %q, %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestReadTokenLine_Cancel(t *testing.T) {
	if _, err := readTokenLine(strings.NewReader("token\x03"), true); err == nil {
		t.Fatal("expected Ctrl+C to cancel hidden input")
	}
}

func TestReadTokenLine_ReadError(t *testing.T) {
	wantErr := errors.New("read failed")
	if _, err := readTokenLine(iotest.ErrReader(wantErr), false); !errors.Is(err, wantErr) {
		t.Fatalf("got %v, want %v", err, wantErr)
	}
}

func TestReadTokenLine_PreservesNextPromptInput(t *testing.T) {
	in := strings.NewReader("token\nnext\n")
	if _, err := readTokenLine(in, false); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(in)
	if err != nil || string(rest) != "next\n" {
		t.Fatalf("remaining input = %q, %v", rest, err)
	}
}

func buildJWT(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}

func TestParseCampaignToken_PlainKeys(t *testing.T) {
	token := buildJWT(map[string]any{
		"edge_system_id": "ces-test",
		"participant_id": "pub-1234",
	})
	svc, part, err := ParseCampaignToken(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc != "ces-test" {
		t.Fatalf("expected ces-test, got %s", svc)
	}
	if part != "pub-1234" {
		t.Fatalf("expected pub-1234, got %s", part)
	}
}

func TestParseCampaignToken_NamespacedKeys(t *testing.T) {
	token := buildJWT(map[string]any{
		"https://devices.cloud.rti.com/edge_system_id": "ces-alpha-abc123",
		"https://devices.cloud.rti.com/participant_id": "publisher-0bddb5dc",
		"iss": "https://auth.dev-rti.com/",
		"sub": "client@clients",
	})
	svc, part, err := ParseCampaignToken(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc != "ces-alpha-abc123" {
		t.Fatalf("expected ces-alpha-abc123, got %s", svc)
	}
	if part != "publisher-0bddb5dc" {
		t.Fatalf("expected publisher-0bddb5dc, got %s", part)
	}
}

func TestParseCampaignToken_MissingKeys(t *testing.T) {
	token := buildJWT(map[string]any{
		"iss": "https://auth.example.com/",
		"sub": "user@example",
	})
	_, _, err := ParseCampaignToken(token)
	if err == nil {
		t.Fatal("expected error for missing keys")
	}
}

func TestParseCampaignToken_InvalidJWT(t *testing.T) {
	_, _, err := ParseCampaignToken("not-a-jwt")
	if err == nil {
		t.Fatal("expected error for malformed JWT")
	}
}

func TestCampaignTokenDeviceDomain_NamespacedKey(t *testing.T) {
	token := buildJWT(map[string]any{
		"https://devices.cloud.rti.com/edge_system_id": "ces-test-1234-dbae91243d494dae923192804329a046",
		"https://devices.cloud.rti.com/participant_id": "default-participant-template",
		"https://devices.cloud.rti.com/device_domain":  "test-1234-dbae91243d494dae923192804329a046.devices.test.cloud.dev-rti.com",
		"iss": "https://auth.dev-rti.com/",
	})
	got := CampaignTokenDeviceDomain(token)
	want := "test-1234-dbae91243d494dae923192804329a046.devices.test.cloud.dev-rti.com"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestCampaignTokenDeviceDomain_PlainKey(t *testing.T) {
	token := buildJWT(map[string]any{
		"edge_system_id": "ces-test",
		"participant_id": "pub-1234",
		"device_domain":  "svc.devices.cloud.rti.com",
	})
	if got := CampaignTokenDeviceDomain(token); got != "svc.devices.cloud.rti.com" {
		t.Fatalf("expected svc.devices.cloud.rti.com, got %q", got)
	}
}

func TestCampaignTokenDeviceDomain_Missing(t *testing.T) {
	token := buildJWT(map[string]any{
		"edge_system_id": "ces-test",
		"participant_id": "pub-1234",
	})
	if got := CampaignTokenDeviceDomain(token); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestCampaignTokenDeviceDomain_InvalidJWT(t *testing.T) {
	if got := CampaignTokenDeviceDomain("not-a-jwt"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestTruncateToken(t *testing.T) {
	short := "abc123"
	if got := truncateToken(short, 60); got != short {
		t.Fatalf("expected %q, got %q", short, got)
	}
	long := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCIsImtpZCI6Ik80Y3dKSDViTU95RjhTd0dtcTF5VCJ9.more"
	got := truncateToken(long, 20)
	runes := []rune(got)
	if len(runes) > 21 { // 20 runes + "…"
		t.Fatalf("expected truncated, got %q (len %d runes)", got, len(runes))
	}
}
