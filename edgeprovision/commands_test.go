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
	runner.NewMTLSClient = func(baseURL, _, _, _ string, _ bool) (*Client, error) {
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
	if err := runner.DeviceStatus("https://x:8443", "cert", "key", "ca"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "alpha") || !strings.Contains(out.String(), "device1") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeviceStatusRequiresMTLSFlags(t *testing.T) {
	runner := newRunnerWithDoer(io.Discard, &fakeDoer{})
	err := runner.DeviceStatus("https://x:8443", "", "", "")
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
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "sensor-net", "device.csr"); err != nil {
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
	if err := runner.RequestIdentity("https://x:8443", "cert", "key", "ca", "sensor-net", ""); err != nil {
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
	if err := runner.RequestPermissions("https://x:8443", "cert", "key", "ca", "sensor-net"); err != nil {
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
	if err := runner.RequestPSK("https://x:8443", "cert", "key", "ca"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "psk_a") || !strings.Contains(out.String(), "psk_b") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestGetCRLToStdout(t *testing.T) {
	doer := &fakeDoer{responses: map[string]*http.Response{
		"GET /sensor-net/crl": newTextResponse(http.StatusOK, "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"),
	}}
	var out bytes.Buffer
	runner := newRunnerWithDoer(&out, doer)
	if err := runner.GetCRL("https://x:8443", "cert", "key", "ca", "sensor-net", ""); err != nil {
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
	if err := runner.GetCRL("https://x:8443", "cert", "key", "ca", "sensor-net", target); err != nil {
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
