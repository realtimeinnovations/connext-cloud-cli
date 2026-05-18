package edgeprovision

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewClient(t *testing.T) {
	c := NewClient("http://localhost:8080")
	if c.BaseURL != "http://localhost:8080" {
		t.Fatalf("unexpected base URL: %s", c.BaseURL)
	}
	if c.HTTPClient == nil {
		t.Fatal("expected non-nil HTTP client")
	}
}

func TestNewClientTrimsTrailingSlash(t *testing.T) {
	c := NewClient("http://localhost:8080/")
	if c.BaseURL != "http://localhost:8080" {
		t.Fatalf("unexpected base URL: %s", c.BaseURL)
	}
}

func TestHealthz(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "healthy": true})
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.Healthz(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ok") || !strings.Contains(out.String(), "healthy") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestSignCSR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/sign" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if payload["csr"] != "dGVzdA==" {
			t.Fatalf("unexpected CSR payload: %v", payload["csr"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"certificate": "-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----",
			"ca_chain":    "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.SignCSR("dGVzdA=="); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "certificate") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeviceStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/device/status" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":      "ok",
			"edge_system": "alpha",
			"client_dn":   "CN=device1.sensor-net",
			"pod":         "ces-alpha-abc",
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.DeviceStatus(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "alpha") || !strings.Contains(out.String(), "device1") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sensor-net/identity" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if _, ok := payload["csr_pem"]; !ok {
			t.Fatal("expected csr_pem in payload")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"identity_cert_pem": "-----BEGIN CERTIFICATE-----\nid\n-----END CERTIFICATE-----",
			"cert_serial":       "ABCDEF",
			"lease":             map[string]any{"not_before": "2026-01-01T00:00:00Z", "not_after": "2026-07-01T00:00:00Z", "renew_after": "2026-05-01T00:00:00Z"},
			"server_time_utc":   "2026-05-16T00:00:00Z",
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte("-----BEGIN CERTIFICATE REQUEST-----\ntest\n-----END CERTIFICATE REQUEST-----"), nil
	}
	if err := runner.RequestIdentity("sensor-net", "device.csr"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ABCDEF") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestIdentityWithoutCSR(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if _, ok := payload["csr_pem"]; ok {
			t.Fatal("did not expect csr_pem for renewal")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"identity_cert_pem": "renewed",
			"cert_serial":       "123",
			"lease":             map[string]any{"not_before": "a", "not_after": "b", "renew_after": "c"},
			"server_time_utc":   "d",
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.RequestIdentity("sensor-net", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "renewed") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestPermissions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sensor-net/permissions" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"permissions_doc_smime": "MIME-Version: 1.0...",
			"subject_name":          "CN=device1.sensor-net",
			"lease":                 map[string]any{"not_before": "a", "not_after": "b", "renew_after": "c"},
			"server_time_utc":       "d",
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.RequestPermissions("sensor-net"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "permissions_doc_smime") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRequestPSK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/psk" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"psk_a": map[string]any{
				"passphrase":    "1:abc",
				"passphrase_id": 1,
				"lease":         map[string]any{"not_before": "a", "not_after": "b"},
			},
			"psk_b": map[string]any{
				"passphrase":    "2:def",
				"passphrase_id": 2,
				"lease":         map[string]any{"not_before": "c", "not_after": "d"},
			},
			"server_time_utc": "e",
		})
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.RequestPSK(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "psk_a") || !strings.Contains(out.String(), "psk_b") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestGetCRLToStdout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sensor-net/crl" || r.Method != http.MethodGet {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write([]byte("-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"))
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.GetCRL("sensor-net", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "BEGIN X509 CRL") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestGetCRLToFile(t *testing.T) {
	crlContent := "-----BEGIN X509 CRL-----\ntest\n-----END X509 CRL-----"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		_, _ = w.Write([]byte(crlContent))
	}))
	defer server.Close()

	var out bytes.Buffer
	target := filepath.Join(t.TempDir(), "crl.pem")
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.GetCRL("sensor-net", target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != crlContent {
		t.Fatalf("unexpected CRL content: %s", data)
	}
	if !strings.Contains(out.String(), "CRL saved to") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"signing failed"}`))
	}))
	defer server.Close()

	var out bytes.Buffer
	runner := NewRunner(NewClient(server.URL), &out)
	if err := runner.Healthz(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Error") || !strings.Contains(out.String(), "500") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}
