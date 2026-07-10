// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgeprovision

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDoer mocks the http.Client used by edgeprovision.Client.  Tests record
// the request that was sent and return a canned response keyed by
// "METHOD path", mirroring the fakeAPI pattern in commands/commands_test.go.
type fakeDoer struct {
	lastMethod string
	lastPath   string
	lastBody   []byte
	responses  map[string]*http.Response
}

func (doer *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	doer.lastMethod = req.Method
	doer.lastPath = req.URL.Path
	if req.Body != nil {
		doer.lastBody, _ = io.ReadAll(req.Body)
	}
	if resp, ok := doer.responses[req.Method+" "+req.URL.Path]; ok {
		return resp, nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing"))}, nil
}

func newJSONResponse(status int, payload any) *http.Response {
	data, _ := json.Marshal(payload)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data))}
}

func newTextResponse(status int, payload string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(payload))}
}

// newRunnerWithDoer returns a Runner whose Client factories produce Clients
// backed by the given fakeDoer, so no real HTTP traffic occurs.
func newRunnerWithDoer(out io.Writer, doer *fakeDoer) *Runner {
	runner := NewRunner(out)
	runner.NewClient = func(baseURL string) *Client {
		return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTPClient: doer}
	}
	runner.NewMTLSClient = func(baseURL, _, _, _, _ string) (*Client, error) {
		return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTPClient: doer}, nil
	}
	return runner
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://localhost:8080/")
	if c.BaseURL != "http://localhost:8080" {
		t.Fatalf("unexpected base URL: %s", c.BaseURL)
	}
	if c.HTTPClient == nil {
		t.Fatal("expected non-nil HTTP client")
	}
}

func TestDeviceStatus(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /device/status": newJSONResponse(http.StatusOK, map[string]any{
			"status":     "ok",
			"edgeSystem": "alpha",
			"clientDn":   "CN=device1.sensor-net",
			"pod":        "ces-alpha-abc",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.DeviceStatus("https://x:8443", "cert", "key", "ca", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "alpha") || !strings.Contains(out.String(), "device1") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeviceStatusRequiresMTLSFlags(t *testing.T) {
	runner := newRunnerWithDoer(io.Discard, &fakeDoer{})
	err := runner.DeviceStatus("https://x:8443", "", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "--cert, --key, and --ca are required") {
		t.Fatalf("expected mTLS flags error, got %v", err)
	}
}

func TestRequestIdentity(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /identity": newJSONResponse(http.StatusOK, map[string]any{
			"identityCertPem": "-----BEGIN CERTIFICATE-----\nid\n-----END CERTIFICATE-----",
			"certSerial":      "ABCDEF",
			"lease":           map[string]any{"notBefore": "2026-01-01T00:00:00Z", "notAfter": "2026-07-01T00:00:00Z", "renewAfter": "2026-05-01T00:00:00Z"},
			"serverTimeUtc":   "2026-05-16T00:00:00Z",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte("-----BEGIN CERTIFICATE REQUEST-----\ntest\n-----END CERTIFICATE REQUEST-----"), nil
	}
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "device.csr", ""); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(doer.lastBody, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["csr_pem"]; !ok {
		t.Fatal("expected csr_pem in payload")
	}
	if !strings.Contains(out.String(), "ABCDEF") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestIdentityWithoutCSR(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /identity": newJSONResponse(http.StatusOK, map[string]any{
			"identityCertPem": "renewed",
			"certSerial":      "123",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "", ""); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(doer.lastBody, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["csr_pem"]; ok {
		t.Fatal("did not expect csr_pem for renewal")
	}
	if !strings.Contains(out.String(), "renewed") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestPermissions(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /permissions": newJSONResponse(http.StatusOK, map[string]any{
			"permissionsDocSmime": "MIME-Version: 1.0...",
			"subjectName":         "CN=device1.sensor-net",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "permissionsDocSmime") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestPSK(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /psk": newJSONResponse(http.StatusOK, map[string]any{
			"pskA": map[string]any{"passphrase": "1:abc", "passphraseId": 1},
			"pskB": map[string]any{"passphrase": "2:def", "passphraseId": 2},
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.RequestPSK("https://x:8443", "cert", "key", "ca", "", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pskA") || !strings.Contains(out.String(), "pskB") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestIdentityToFile(t *testing.T) {
	const certPEM = "-----BEGIN CERTIFICATE-----\nid\n-----END CERTIFICATE-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /identity": newJSONResponse(http.StatusOK, map[string]any{
			"identityCertPem": certPEM,
			"certSerial":      "ABC",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	target := filepath.Join(t.TempDir(), "subdir", "identity.pem")
	var mkdirPath, wrotePath string
	var wroteData []byte
	runner.MkdirAll = func(path string, _ os.FileMode) error { mkdirPath = path; return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		wrotePath = path
		wroteData = data
		return nil
	}
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "", target); err != nil {
		t.Fatal(err)
	}
	if mkdirPath != filepath.Dir(target) {
		t.Fatalf("expected MkdirAll on parent dir, got %q", mkdirPath)
	}
	if wrotePath != target || string(wroteData) != certPEM {
		t.Fatalf("unexpected write: path=%q data=%s", wrotePath, wroteData)
	}
	if !strings.Contains(out.String(), "Identity certificate saved to") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestRequestIdentityToFileWritesLease(t *testing.T) {
	const certPEM = "-----BEGIN CERTIFICATE-----\nid\n-----END CERTIFICATE-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /identity": newJSONResponse(http.StatusOK, map[string]any{
			"identityCertPem": certPEM,
			"certSerial":      "ABC",
			"lease":           map[string]any{"notBefore": "2026-01-01T00:00:00Z", "notAfter": "2026-07-01T00:00:00Z"},
			"serverTimeUtc":   "2026-05-16T00:00:00Z",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	target := filepath.Join(t.TempDir(), "subdir", "identity.crt")
	written := map[string][]byte{}
	runner.MkdirAll = func(path string, _ os.FileMode) error { return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		written[filepath.Base(path)] = data
		return nil
	}
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "", target); err != nil {
		t.Fatal(err)
	}
	if written["identity.crt"] == nil {
		t.Fatal("expected identity.crt to be written")
	}
	if !strings.Contains(string(written["identity.lease.json"]), "notAfter") {
		t.Fatalf("expected identity.lease.json with lease data; got %s", written["identity.lease.json"])
	}
	if !strings.Contains(string(written["identity.lease.json"]), "serverTimeUtc") {
		t.Fatalf("expected identity.lease.json with server_time_utc; got %s", written["identity.lease.json"])
	}
	if !strings.Contains(out.String(), "Identity lease saved to") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestRequestPermissionsToFile(t *testing.T) {
	const doc = "MIME-Version: 1.0\r\ncontent"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /permissions": newJSONResponse(http.StatusOK, map[string]any{
			"permissionsDocSmime": doc,
			"subjectName":         "CN=device1.net",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	target := filepath.Join(t.TempDir(), "subdir", "permissions.p7s")
	var mkdirPath, wrotePath string
	var wroteData []byte
	runner.MkdirAll = func(path string, _ os.FileMode) error { mkdirPath = path; return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		wrotePath = path
		wroteData = data
		return nil
	}
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "", target); err != nil {
		t.Fatal(err)
	}
	if mkdirPath != filepath.Dir(target) {
		t.Fatalf("expected MkdirAll on parent dir, got %q", mkdirPath)
	}
	if wrotePath != target || string(wroteData) != doc {
		t.Fatalf("unexpected write: path=%q data=%s", wrotePath, wroteData)
	}
	if !strings.Contains(out.String(), "Permissions document saved to") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestRequestPermissionsToFileWritesLease(t *testing.T) {
	const doc = "MIME-Version: 1.0\r\ncontent"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /permissions": newJSONResponse(http.StatusOK, map[string]any{
			"permissionsDocSmime": doc,
			"subjectName":         "CN=device1.net",
			"lease":               map[string]any{"notBefore": "2026-01-01T00:00:00Z", "notAfter": "2026-07-01T00:00:00Z"},
			"serverTimeUtc":       "2026-05-16T00:00:00Z",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	target := filepath.Join(t.TempDir(), "subdir", "permissions.p7s")
	written := map[string][]byte{}
	runner.MkdirAll = func(path string, _ os.FileMode) error { return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		written[filepath.Base(path)] = data
		return nil
	}
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "", target); err != nil {
		t.Fatal(err)
	}
	if written["permissions.p7s"] == nil {
		t.Fatal("expected permissions file to be written")
	}
	if !strings.Contains(string(written["permissions.lease.json"]), "notAfter") {
		t.Fatalf("expected permissions.lease.json with lease data; got %s", written["permissions.lease.json"])
	}
	if !strings.Contains(out.String(), "Permissions lease saved to") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestRequestPSKToFile(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /psk": newJSONResponse(http.StatusOK, map[string]any{
			"pskA":          map[string]any{"passphrase": "0:aaaa", "passphraseId": 0, "lease": map[string]any{"notBefore": "2026-01-01T00:00:00Z", "notAfter": "2026-01-02T00:00:00Z"}},
			"pskB":          map[string]any{"passphrase": "1:bbbb", "passphraseId": 1, "lease": map[string]any{"notBefore": "2026-01-01T00:00:00Z", "notAfter": "2026-01-03T00:00:00Z"}},
			"serverTimeUtc": "2026-01-01T00:00:00Z",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	// Supply a non-directory path so pskOutputDir derives its parent as the dir.
	target := filepath.Join(t.TempDir(), "subdir", "psk.json")
	expectedDir := filepath.Dir(target)
	written := map[string][]byte{}
	runner.MkdirAll = func(path string, _ os.FileMode) error { return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		written[filepath.Base(path)] = data
		return nil
	}
	if err := runner.RequestPSK("https://x:8443", "cert", "key", "ca", "", target); err != nil {
		t.Fatal(err)
	}
	_ = expectedDir // dir is implied by filepath.Base checks below
	if string(written["psk_secret.key"]) != "0:aaaa" {
		t.Fatalf("unexpected psk_secret.key: %q", written["psk_secret.key"])
	}
	if string(written["psk_secret_extra.key"]) != "0:aaaa\n1:bbbb" {
		t.Fatalf("unexpected psk_secret_extra.key: %q", written["psk_secret_extra.key"])
	}
	if !strings.Contains(string(written["psk_secret.lease.json"]), "pskA") || !strings.Contains(string(written["psk_secret.lease.json"]), "serverTimeUtc") {
		t.Fatalf("unexpected psk_secret.lease.json: %s", written["psk_secret.lease.json"])
	}
	if !strings.Contains(out.String(), "PSK primary passphrase saved") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestGetCRLToStdout(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /crl": newTextResponse(http.StatusOK, "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.GetCRL("https://x:8443", "cert", "key", "ca", "", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "BEGIN X509 CRL") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestGetCRLToFileCreatesParentDir(t *testing.T) {
	crlContent := "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /crl": newTextResponse(http.StatusOK, crlContent),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	target := filepath.Join(t.TempDir(), "nested", "subdir", "crl.pem")
	var mkdirPath string
	var wrotePath string
	var wroteData []byte
	runner.MkdirAll = func(path string, _ os.FileMode) error { mkdirPath = path; return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		wrotePath = path
		wroteData = data
		return nil
	}
	if err := runner.GetCRL("https://x:8443", "cert", "key", "ca", "", target); err != nil {
		t.Fatal(err)
	}
	if mkdirPath != filepath.Dir(target) {
		t.Fatalf("expected MkdirAll on parent dir, got %q", mkdirPath)
	}
	if wrotePath != target || string(wroteData) != crlContent {
		t.Fatalf("unexpected write: %q / %s", wrotePath, wroteData)
	}
	if !strings.Contains(out.String(), "CRL saved to") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

// ── Directory-output tests ────────────────────────────────────────────────────
// These verify that passing an existing directory as --output causes the CLI to
// write the artifact to a default filename inside that directory.

func TestRequestIdentityToDirectory(t *testing.T) {
	const certPEM = "-----BEGIN CERTIFICATE-----\nid\n-----END CERTIFICATE-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /identity": newJSONResponse(http.StatusOK, map[string]any{
			"identityCertPem": certPEM,
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	dir := t.TempDir()
	var wrotePath string
	var wroteData []byte
	runner.MkdirAll = func(path string, _ os.FileMode) error { return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		wrotePath = path
		wroteData = data
		return nil
	}
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "", dir); err != nil {
		t.Fatal(err)
	}
	if wrotePath != filepath.Join(dir, "identity.crt") {
		t.Fatalf("expected identity.crt in dir, got %q", wrotePath)
	}
	if string(wroteData) != certPEM {
		t.Fatalf("unexpected cert content: %s", wroteData)
	}
}

func TestRequestPermissionsToDirectory(t *testing.T) {
	const doc = "MIME-Version: 1.0\r\ncontent"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /permissions": newJSONResponse(http.StatusOK, map[string]any{
			"permissionsDocSmime": doc,
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	dir := t.TempDir()
	var wrotePath string
	runner.MkdirAll = func(path string, _ os.FileMode) error { return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		wrotePath = path
		return nil
	}
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "", dir); err != nil {
		t.Fatal(err)
	}
	if wrotePath != filepath.Join(dir, "permissions.p7s") {
		t.Fatalf("expected permissions.p7s in dir, got %q", wrotePath)
	}
}

func TestRequestPSKToDirectory(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /psk": newJSONResponse(http.StatusOK, map[string]any{
			"pskA": map[string]any{"passphrase": "0:abc", "passphraseId": 0},
			"pskB": map[string]any{"passphrase": "1:def", "passphraseId": 1},
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	dir := t.TempDir()
	written := map[string]bool{}
	runner.MkdirAll = func(path string, _ os.FileMode) error { return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		written[filepath.Base(path)] = true
		return nil
	}
	if err := runner.RequestPSK("https://x:8443", "cert", "key", "ca", "", dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"psk_secret.key", "psk_secret_extra.key", "psk_secret.lease.json"} {
		if !written[f] {
			t.Fatalf("expected %s to be written; got %v", f, written)
		}
	}
}

func TestGetCRLToDirectory(t *testing.T) {
	crlContent := "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /crl": newTextResponse(http.StatusOK, crlContent),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	dir := t.TempDir()
	var wrotePath string
	runner.MkdirAll = func(path string, _ os.FileMode) error { return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		wrotePath = path
		return nil
	}
	if err := runner.GetCRL("https://x:8443", "cert", "key", "ca", "", dir); err != nil {
		t.Fatal(err)
	}
	if wrotePath != filepath.Join(dir, "crl.pem") {
		t.Fatalf("expected crl.pem in dir, got %q", wrotePath)
	}
}

func TestRenewDeviceCert(t *testing.T) {
	const certPEM = "-----BEGIN CERTIFICATE-----\nnewcert\n-----END CERTIFICATE-----"
	const caPEM = "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /device/renew-cert": newJSONResponse(http.StatusOK, map[string]any{
			"certificate": certPEM,
			"caChain":     caPEM,
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	runner.ReadFile = func(_ string) ([]byte, error) {
		return []byte("-----BEGIN CERTIFICATE REQUEST-----\ncsr\n-----END CERTIFICATE REQUEST-----"), nil
	}
	if err := runner.RenewDeviceCert("https://x:8443", "cert", "key", "ca", "", "device.csr", 0, ""); err != nil {
		t.Fatal(err)
	}
	if doer.lastMethod != http.MethodPost || doer.lastPath != "/device/renew-cert" {
		t.Fatalf("unexpected request: %s %s", doer.lastMethod, doer.lastPath)
	}
	var payload map[string]any
	if err := json.Unmarshal(doer.lastBody, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["csr"]; !ok {
		t.Fatal("expected 'csr' key in request payload")
	}
	if _, ok := payload["validity_minutes"]; ok {
		t.Fatal("validity_minutes should not be sent when 0")
	}
	if !strings.Contains(out.String(), "newcert") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRenewDeviceCertWithValidityMinutes(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /device/renew-cert": newJSONResponse(http.StatusOK, map[string]any{
			"certificate": "-----BEGIN CERTIFICATE-----\ncert\n-----END CERTIFICATE-----",
			"caChain":     "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
		}),
	}}
	runner := newRunnerWithDoer(io.Discard, doer)
	runner.ReadFile = func(_ string) ([]byte, error) { return []byte("CSR"), nil }
	if err := runner.RenewDeviceCert("https://x:8443", "cert", "key", "ca", "", "device.csr", 1440, ""); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(doer.lastBody, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["validity_minutes"] != float64(1440) {
		t.Fatalf("expected validity_minutes=1440, got %v", payload["validity_minutes"])
	}
}

func TestRenewDeviceCertToDirectory(t *testing.T) {
	const certPEM = "-----BEGIN CERTIFICATE-----\nnewcert\n-----END CERTIFICATE-----"
	const caPEM = "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /device/renew-cert": newJSONResponse(http.StatusOK, map[string]any{
			"certificate": certPEM,
			"caChain":     caPEM,
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	runner.ReadFile = func(_ string) ([]byte, error) { return []byte("CSR"), nil }
	dir := t.TempDir()
	written := map[string][]byte{}
	runner.MkdirAll = func(path string, _ os.FileMode) error { return nil }
	runner.WriteFile = func(path string, data []byte, _ os.FileMode) error {
		written[filepath.Base(path)] = data
		return nil
	}
	if err := runner.RenewDeviceCert("https://x:8443", "cert", "key", "ca", "", "device.csr", 0, dir); err != nil {
		t.Fatal(err)
	}
	if string(written["node.crt"]) != certPEM {
		t.Fatalf("unexpected node.crt: %s", written["node.crt"])
	}
	if string(written["ca-chain.crt"]) != caPEM {
		t.Fatalf("unexpected ca-chain.crt: %s", written["ca-chain.crt"])
	}
	if !strings.Contains(out.String(), "Device certificate saved to") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
	if !strings.Contains(out.String(), "CA chain saved to") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestRenewDeviceCertRequiresMTLS(t *testing.T) {
	runner := newRunnerWithDoer(io.Discard, &fakeDoer{})
	runner.ReadFile = func(_ string) ([]byte, error) { return []byte("CSR"), nil }
	err := runner.RenewDeviceCert("https://x:8443", "", "", "", "", "device.csr", 0, "")
	if err == nil || !strings.Contains(err.Error(), "--cert, --key, and --ca are required") {
		t.Fatalf("expected mTLS flags error, got %v", err)
	}
}

func TestRenewDeviceCertHTTPError(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /device/renew-cert": newJSONResponse(http.StatusUnprocessableEntity, map[string]any{
			"error": "Invalid request.",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	runner.ReadFile = func(_ string) ([]byte, error) { return []byte("CSR"), nil }
	err := runner.RenewDeviceCert("https://x:8443", "cert", "key", "ca", "", "device.csr", 0, "/tmp/out/")
	if err == nil {
		t.Fatal("expected error from HTTP 422")
	}
	if !strings.Contains(out.String(), "Error:") {
		t.Fatalf("expected error message in output: %s", out.String())
	}
}
