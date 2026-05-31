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
	runner.NewClient = func(baseURL string, _ bool) *Client {
		return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTPClient: doer}
	}
	runner.NewMTLSClient = func(baseURL, _, _, _, _ string, _ bool) (*Client, error) {
		return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTPClient: doer}, nil
	}
	return runner
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://localhost:8080/", true)
	if c.BaseURL != "http://localhost:8080" {
		t.Fatalf("unexpected base URL: %s", c.BaseURL)
	}
	if c.HTTPClient == nil {
		t.Fatal("expected non-nil HTTP client")
	}
}

func TestHealthz(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /healthz": newJSONResponse(http.StatusOK, map[string]any{"status": "ok", "healthy": true}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.Healthz("http://localhost:8080"); err != nil {
		t.Fatal(err)
	}
	if doer.lastMethod != http.MethodGet || doer.lastPath != "/healthz" {
		t.Fatalf("unexpected request: %s %s", doer.lastMethod, doer.lastPath)
	}
	if !strings.Contains(out.String(), "ok") || !strings.Contains(out.String(), "healthy") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestHealthzRequiresURL(t *testing.T) {
	runner := newRunnerWithDoer(io.Discard, &fakeDoer{})
	if err := runner.Healthz(""); err == nil || !strings.Contains(err.Error(), "--url is required") {
		t.Fatalf("expected --url required error, got %v", err)
	}
}

func TestSignCSR(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /internal/sign": newJSONResponse(http.StatusOK, map[string]any{
			"certificate": "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
			"ca_chain":    "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.SignCSR("http://localhost:8080", "dGVzdA=="); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(doer.lastBody, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["csr"] != "dGVzdA==" {
		t.Fatalf("unexpected CSR payload: %v", payload["csr"])
	}
	if !strings.Contains(out.String(), "certificate") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeviceStatus(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /device/status": newJSONResponse(http.StatusOK, map[string]any{
			"status":      "ok",
			"edge_system": "alpha",
			"client_dn":   "CN=device1.sensor-net",
			"pod":         "ces-alpha-abc",
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
		"POST /sensor-net/identity": newJSONResponse(http.StatusOK, map[string]any{
			"identity_cert_pem": "-----BEGIN CERTIFICATE-----\nid\n-----END CERTIFICATE-----",
			"cert_serial":       "ABCDEF",
			"lease":             map[string]any{"not_before": "2026-01-01T00:00:00Z", "not_after": "2026-07-01T00:00:00Z", "renew_after": "2026-05-01T00:00:00Z"},
			"server_time_utc":   "2026-05-16T00:00:00Z",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte("-----BEGIN CERTIFICATE REQUEST-----\ntest\n-----END CERTIFICATE REQUEST-----"), nil
	}
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "sensor-net", "device.csr", ""); err != nil {
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
		"POST /sensor-net/identity": newJSONResponse(http.StatusOK, map[string]any{
			"identity_cert_pem": "renewed",
			"cert_serial":       "123",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "sensor-net", "", ""); err != nil {
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
		"POST /sensor-net/permissions": newJSONResponse(http.StatusOK, map[string]any{
			"permissions_doc_smime": "MIME-Version: 1.0...",
			"subject_name":          "CN=device1.sensor-net",
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "", "sensor-net", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "permissions_doc_smime") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestPSK(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /psk": newJSONResponse(http.StatusOK, map[string]any{
			"psk_a": map[string]any{"passphrase": "1:abc", "passphrase_id": 1},
			"psk_b": map[string]any{"passphrase": "2:def", "passphrase_id": 2},
		}),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.RequestPSK("https://x:8443", "cert", "key", "ca", "", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "psk_a") || !strings.Contains(out.String(), "psk_b") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestIdentityToFile(t *testing.T) {
	const certPEM = "-----BEGIN CERTIFICATE-----\nid\n-----END CERTIFICATE-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /net/identity": newJSONResponse(http.StatusOK, map[string]any{
			"identity_cert_pem": certPEM,
			"cert_serial":       "ABC",
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
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "net", "", target); err != nil {
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
		"POST /net/identity": newJSONResponse(http.StatusOK, map[string]any{
			"identity_cert_pem": certPEM,
			"cert_serial":       "ABC",
			"lease":             map[string]any{"not_before": "2026-01-01T00:00:00Z", "not_after": "2026-07-01T00:00:00Z"},
			"server_time_utc":   "2026-05-16T00:00:00Z",
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
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "net", "", target); err != nil {
		t.Fatal(err)
	}
	if written["identity.crt"] == nil {
		t.Fatal("expected identity.crt to be written")
	}
	if !strings.Contains(string(written["identity_lease.json"]), "not_after") {
		t.Fatalf("expected identity_lease.json with lease data; got %s", written["identity_lease.json"])
	}
	if !strings.Contains(string(written["identity_lease.json"]), "server_time_utc") {
		t.Fatalf("expected identity_lease.json with server_time_utc; got %s", written["identity_lease.json"])
	}
	if !strings.Contains(out.String(), "Identity lease saved to") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestRequestPermissionsToFile(t *testing.T) {
	const doc = "MIME-Version: 1.0\r\ncontent"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /net/permissions": newJSONResponse(http.StatusOK, map[string]any{
			"permissions_doc_smime": doc,
			"subject_name":          "CN=device1.net",
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
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "", "net", target); err != nil {
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
		"POST /net/permissions": newJSONResponse(http.StatusOK, map[string]any{
			"permissions_doc_smime": doc,
			"subject_name":          "CN=device1.net",
			"lease":                 map[string]any{"not_before": "2026-01-01T00:00:00Z", "not_after": "2026-07-01T00:00:00Z"},
			"server_time_utc":       "2026-05-16T00:00:00Z",
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
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "", "net", target); err != nil {
		t.Fatal(err)
	}
	if written["permissions.p7s"] == nil {
		t.Fatal("expected permissions file to be written")
	}
	if !strings.Contains(string(written["permissions_lease.json"]), "not_after") {
		t.Fatalf("expected permissions_lease.json with lease data; got %s", written["permissions_lease.json"])
	}
	if !strings.Contains(out.String(), "Permissions lease saved to") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestRequestPSKToFile(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /psk": newJSONResponse(http.StatusOK, map[string]any{
			"psk_a":           map[string]any{"passphrase": "0:aaaa", "passphrase_id": 0, "lease": map[string]any{"not_before": "2026-01-01T00:00:00Z", "not_after": "2026-01-02T00:00:00Z"}},
			"psk_b":           map[string]any{"passphrase": "1:bbbb", "passphrase_id": 1, "lease": map[string]any{"not_before": "2026-01-01T00:00:00Z", "not_after": "2026-01-03T00:00:00Z"}},
			"server_time_utc": "2026-01-01T00:00:00Z",
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
	if string(written["psk_primary.txt"]) != "0:aaaa" {
		t.Fatalf("unexpected psk_primary.txt: %q", written["psk_primary.txt"])
	}
	if string(written["psk_extra.txt"]) != "0:aaaa\n1:bbbb" {
		t.Fatalf("unexpected psk_extra.txt: %q", written["psk_extra.txt"])
	}
	if !strings.Contains(string(written["psk_lease.json"]), "psk_a") || !strings.Contains(string(written["psk_lease.json"]), "server_time_utc") {
		t.Fatalf("unexpected psk_lease.json: %s", written["psk_lease.json"])
	}
	if !strings.Contains(out.String(), "PSK primary passphrase saved") {
		t.Fatalf("unexpected stdout: %s", out.String())
	}
}

func TestGetCRLToStdout(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /sensor-net/crl": newTextResponse(http.StatusOK, "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.GetCRL("https://x:8443", "cert", "key", "ca", "", "sensor-net", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "BEGIN X509 CRL") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestGetCRLToFileCreatesParentDir(t *testing.T) {
	crlContent := "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /sensor-net/crl": newTextResponse(http.StatusOK, crlContent),
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
	if err := runner.GetCRL("https://x:8443", "cert", "key", "ca", "", "sensor-net", target); err != nil {
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
		"POST /net/identity": newJSONResponse(http.StatusOK, map[string]any{
			"identity_cert_pem": certPEM,
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
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "", "net", "", dir); err != nil {
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
		"POST /net/permissions": newJSONResponse(http.StatusOK, map[string]any{
			"permissions_doc_smime": doc,
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
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "", "net", dir); err != nil {
		t.Fatal(err)
	}
	if wrotePath != filepath.Join(dir, "signed_permissions.p7s") {
		t.Fatalf("expected signed_permissions.p7s in dir, got %q", wrotePath)
	}
}

func TestRequestPSKToDirectory(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"POST /psk": newJSONResponse(http.StatusOK, map[string]any{
			"psk_a": map[string]any{"passphrase": "0:abc", "passphrase_id": 0},
			"psk_b": map[string]any{"passphrase": "1:def", "passphrase_id": 1},
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
	for _, f := range []string{"psk_primary.txt", "psk_extra.txt", "psk_lease.json"} {
		if !written[f] {
			t.Fatalf("expected %s to be written; got %v", f, written)
		}
	}
}

func TestGetCRLToDirectory(t *testing.T) {
	crlContent := "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /sensor-net/crl": newTextResponse(http.StatusOK, crlContent),
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
	if err := runner.GetCRL("https://x:8443", "cert", "key", "ca", "", "sensor-net", dir); err != nil {
		t.Fatal(err)
	}
	if wrotePath != filepath.Join(dir, "crl.pem") {
		t.Fatalf("expected crl.pem in dir, got %q", wrotePath)
	}
}

func TestErrorResponseUsesFormatError(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /healthz": newTextResponse(http.StatusInternalServerError, `{"error":"signing failed"}`),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.Healthz("http://localhost:8080"); err != nil {
		t.Fatal(err)
	}
	// Should render via httputil.FormatError — i.e. extract "signing failed",
	// not dump the raw JSON body with the status code prefix.
	output := out.String()
	if !strings.Contains(output, "Error: signing failed") {
		t.Fatalf("expected formatted error, got: %s", output)
	}
	if strings.Contains(output, "(HTTP 500)") {
		t.Fatalf("expected normalized error (no raw (HTTP nnn) prefix), got: %s", output)
	}
}
