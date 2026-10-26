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
	"reflect"
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

// Artifact responses are external fixtures; all output paths and writes use the
// production filesystem code, including missing parent directories.
func TestArtifactFiles(t *testing.T) {
	lease := map[string]any{"notBefore": "2026-01-01T00:00:00Z", "notAfter": "2026-07-01T00:00:00Z"}
	leaseResponse := map[string]any{"lease": lease, "serverTimeUtc": "2026-05-16T00:00:00Z"}
	pskLease := map[string]any{"pskA": map[string]any{"lease": lease}, "pskB": map[string]any{"lease": lease}, "serverTimeUtc": "2026-05-16T00:00:00Z"}
	for _, tc := range []struct {
		name, request, filename, leaseFile string
		response                           any
		text                               string
		files                              map[string]string
		lease                              any
		run                                func(*Runner, string, string) error
	}{
		{name: "identity", request: "POST /identity", filename: "identity.crt", response: map[string]any{"identityCertPem": "CERT"}, files: map[string]string{"identity.crt": "CERT"}, run: func(r *Runner, output, csr string) error {
			return r.RequestIdentity("https://device.test", "cert", "key", "ca", "", csr, output)
		}},
		{name: "identity with lease", request: "POST /identity", filename: "identity.crt", response: map[string]any{"identityCertPem": "CERT", "lease": lease, "serverTimeUtc": "2026-05-16T00:00:00Z"}, files: map[string]string{"identity.crt": "CERT"}, leaseFile: "identity.lease.json", lease: leaseResponse, run: func(r *Runner, output, csr string) error {
			return r.RequestIdentity("https://device.test", "cert", "key", "ca", "", csr, output)
		}},
		{name: "permissions", request: "POST /permissions", filename: "permissions.p7s", response: map[string]any{"permissionsDocSmime": "MIME-Version: 1.0\r\ncontent"}, files: map[string]string{"permissions.p7s": "MIME-Version: 1.0\r\ncontent"}, run: func(r *Runner, output, _ string) error {
			return r.RequestPermissions("https://device.test", "cert", "key", "ca", "", output)
		}},
		{name: "permissions with lease", request: "POST /permissions", filename: "permissions.p7s", response: map[string]any{"permissionsDocSmime": "PERMISSIONS", "lease": lease, "serverTimeUtc": "2026-05-16T00:00:00Z"}, files: map[string]string{"permissions.p7s": "PERMISSIONS"}, leaseFile: "permissions.lease.json", lease: leaseResponse, run: func(r *Runner, output, _ string) error {
			return r.RequestPermissions("https://device.test", "cert", "key", "ca", "", output)
		}},
		{name: "PSK", request: "POST /psk", filename: "psk.json", response: map[string]any{"pskA": map[string]any{"passphrase": "0:aaaa", "passphraseId": 0, "lease": lease}, "pskB": map[string]any{"passphrase": "1:bbbb", "passphraseId": 1, "lease": lease}, "serverTimeUtc": "2026-05-16T00:00:00Z"}, files: map[string]string{"psk_secret.key": "0:aaaa", "psk_secret_extra.key": "0:aaaa\n1:bbbb"}, leaseFile: "psk_secret.lease.json", lease: pskLease, run: func(r *Runner, output, _ string) error {
			return r.RequestPSK("https://device.test", "cert", "key", "ca", "", output)
		}},
		{name: "CRL", request: "GET /crl", filename: "crl.pem", text: "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----", files: map[string]string{"crl.pem": "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"}, run: func(r *Runner, output, _ string) error {
			return r.GetCRL("https://device.test", "cert", "key", "ca", "", output)
		}},
		{name: "device certificate", request: "POST /device/renew-cert", response: map[string]any{"certificate": "NEW CERT", "caChain": "CA"}, files: map[string]string{"node.crt": "NEW CERT", "ca-chain.crt": "CA"}, run: func(r *Runner, output, csr string) error {
			return r.RenewDeviceCert("https://device.test", "cert", "key", "ca", "", csr, 0, output)
		}},
	} {
		for _, layout := range []string{"filename", "existing directory", "new directory"} {
			if layout == "filename" && tc.filename == "" {
				continue
			}
			t.Run(tc.name+"/"+layout, func(t *testing.T) {
				root := t.TempDir()
				dir := filepath.Join(root, "nested", "artifacts")
				output := dir + string(os.PathSeparator)
				if layout == "filename" {
					output = filepath.Join(dir, "custom-"+tc.filename)
				}
				if layout == "existing directory" {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					output = dir
				}
				csr := filepath.Join(root, "device.csr")
				if err := os.WriteFile(csr, []byte("CSR CONTENT"), 0600); err != nil {
					t.Fatal(err)
				}
				response := newJSONResponse(http.StatusOK, tc.response)
				if tc.text != "" {
					response = newTextResponse(http.StatusOK, tc.text)
				}
				doer := &fakeDoer{responses: map[string]*http.Response{tc.request: response}}
				var out bytes.Buffer
				runner := newRunnerWithDoer(&out, doer)
				if err := tc.run(runner, output, csr); err != nil {
					t.Fatal(err)
				}
				wantCount := len(tc.files)
				if tc.leaseFile != "" {
					wantCount++
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != wantCount {
					t.Fatalf("artifact directory = %v, error=%v; want %d files", entries, err, wantCount)
				}
				for name, want := range tc.files {
					if layout == "filename" && name == tc.filename {
						name = "custom-" + name
					}
					path := filepath.Join(dir, name)
					data, err := os.ReadFile(path)
					if err != nil || string(data) != want {
						t.Fatalf("%s = %q, error=%v; want %q", path, data, err, want)
					}
					if !strings.Contains(out.String(), path) {
						t.Errorf("saved path missing from output: %s", path)
					}
				}
				if tc.leaseFile != "" {
					data, err := os.ReadFile(filepath.Join(dir, tc.leaseFile))
					if err != nil {
						t.Fatal(err)
					}
					var got any
					if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, tc.lease) {
						t.Fatalf("lease = %s, error=%v; want %v", data, err, tc.lease)
					}
				}
				if tc.request == "POST /identity" || tc.request == "POST /device/renew-cert" {
					var payload map[string]string
					if err := json.Unmarshal(doer.lastBody, &payload); err != nil {
						t.Fatal(err)
					}
					key, want := "csr_pem", "CSR CONTENT"
					if tc.request == "POST /device/renew-cert" {
						key, want = "csr", "Q1NSIENPTlRFTlQ="
					}
					if payload[key] != want {
						t.Fatalf("CSR payload = %v; want %s=%q", payload, key, want)
					}
				}
			})
		}
	}
}
