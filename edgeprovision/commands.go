// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package edgeprovision

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

// Runner executes agent commands against the Provisioning Service API.
// One Runner is created at startup and shared across subcommands; each
// command builds its own Client on demand (mTLS or plain) because cert/key/ca
// and base URL come from the user on the command line.
type Runner struct {
	Out io.Writer
	// Debug, when true, logs every HTTP request and response body to Out.
	Debug bool

	// Injectable file I/O for testability — mirrors commands.Runner.
	ReadFile  func(string) ([]byte, error)
	WriteFile func(string, []byte, os.FileMode) error
	MkdirAll  func(string, os.FileMode) error

	// Client factories — overridable in tests with a fake Doer.
	NewClient     func(baseURL string) *Client
	NewMTLSClient func(baseURL, certFile, keyFile, caFile, serverAddr string) (*Client, error)
}

// NewRunner creates a Runner with sensible defaults.  All Provisioning Service
// endpoints use TLS and require certificate verification, so verification is
// always enabled.
func NewRunner(out io.Writer) *Runner {
	return &Runner{
		Out:           out,
		ReadFile:      os.ReadFile,
		WriteFile:     os.WriteFile,
		MkdirAll:      os.MkdirAll,
		NewClient:     NewClient,
		NewMTLSClient: NewMTLSClient,
	}
}

// mtlsClient builds an mTLS client for the device-facing endpoints.  All three
// of cert/key/ca must be provided together.  serverAddr, when non-empty, overrides
// the TCP dial target (equivalent to curl's --connect-to) while preserving TLS SNI.
func (runner *Runner) mtlsClient(url, certFile, keyFile, caFile, serverAddr string) (*Client, error) {
	if url == "" {
		return nil, fmt.Errorf("--url is required")
	}
	if certFile == "" || keyFile == "" || caFile == "" {
		return nil, fmt.Errorf("--cert, --key, and --ca are required for mTLS endpoints")
	}
	c, err := runner.NewMTLSClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return nil, err
	}
	if runner.Debug {
		c.DebugOut = runner.Out
	}
	return c, nil
}

// decodeBody reads resp, handles non-200 errors, and unmarshals the body into T.
func decodeBody[T any](out io.Writer, resp *http.Response) (T, error) {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var zero T
	if resp.StatusCode != http.StatusOK {
		msg := httputil.FormatError(resp.StatusCode, body)
		_, _ = fmt.Fprintf(out, "Error: %s\n", msg)
		return zero, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return zero, err
	}
	return result, nil
}

// emitJSON consumes resp, prints either the formatted JSON body on success or
// a normalized "Error: …" line on failure.
func (runner *Runner) emitJSON(resp *http.Response) error {
	payload, err := decodeBody[any](runner.Out, resp)
	if err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

// decodeResult consumes resp and returns the decoded JSON object on a 200
// response.  Use this for handlers that need to pick individual fields out of
// the body before writing them to disk.
func (runner *Runner) decodeResult(resp *http.Response) (map[string]any, error) {
	return decodeBody[map[string]any](runner.Out, resp)
}

// DeviceStatus calls GET /device/status (mTLS required).
func (runner *Runner) DeviceStatus(url, certFile, keyFile, caFile, serverAddr string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	resp, err := client.Get("/device/status")
	if err != nil {
		return err
	}
	return runner.emitJSON(resp)
}

// saveToFile writes data to outputPath, creating parent directories as needed.
func (runner *Runner) saveToFile(outputPath string, data []byte) error {
	if dir := filepath.Dir(outputPath); dir != "." {
		if err := runner.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return runner.WriteFile(outputPath, data, 0o644)
}

// extractLease builds a summary map containing only the "lease" and
// "server_time_utc" keys from a JSON response.  Returns nil when neither key
// is present.
func extractLease(result map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := result["lease"]; ok {
		out["lease"] = v
	}
	if v, ok := result["serverTimeUtc"]; ok {
		out["serverTimeUtc"] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// pskOutputDir returns the directory in which the PSK output files should be
// written.  Unlike resolveOutputPath it always treats the argument as a
// directory reference: if output already ends with the path separator or
// points to an existing directory it is used as-is; otherwise the parent
// directory of the supplied path is returned.
func pskOutputDir(output string) string {
	if strings.HasSuffix(output, string(filepath.Separator)) {
		return strings.TrimSuffix(output, string(filepath.Separator))
	}
	if info, err := os.Stat(output); err == nil && info.IsDir() {
		return output
	}
	return filepath.Dir(output)
}

// resolveOutputPath returns a concrete file path for the given output value.
// If output ends with the OS path separator, or points to an existing
// directory, it is treated as a directory and defaultFilename is appended.
// Otherwise output is returned unchanged (caller-specified file path).
func resolveOutputPath(output, defaultFilename string) string {
	if strings.HasSuffix(output, string(filepath.Separator)) {
		return filepath.Join(output, defaultFilename)
	}
	if info, err := os.Stat(output); err == nil && info.IsDir() {
		return filepath.Join(output, defaultFilename)
	}
	return output
}

// RequestIdentity calls POST /identity to issue or renew an identity
// certificate (mTLS required).  If output is non-empty the identity_cert_pem
// field is written to that path; otherwise the full JSON response is printed
// to stdout.
func (runner *Runner) RequestIdentity(url, certFile, keyFile, caFile, serverAddr, csrFile, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	payload := map[string]any{}
	if csrFile != "" {
		data, err := runner.ReadFile(csrFile)
		if err != nil {
			return fmt.Errorf("reading CSR file: %w", err)
		}
		payload["csr_pem"] = string(data)
	}
	resp, err := client.Post("/identity", payload)
	if err != nil {
		return err
	}
	if output == "" {
		return runner.emitJSON(resp)
	}
	result, err := runner.decodeResult(resp)
	if err != nil {
		return err
	}
	certPEM, _ := result["identityCertPem"].(string)
	dest := resolveOutputPath(output, "identity.crt")
	if err := runner.saveToFile(dest, []byte(certPEM)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "Identity certificate saved to %s\n", dest)
	if leaseData := extractLease(result); leaseData != nil {
		leaseJSON, _ := json.MarshalIndent(leaseData, "", "  ")
		leaseDest := filepath.Join(filepath.Dir(dest), "identity.lease.json")
		if err := runner.saveToFile(leaseDest, append(leaseJSON, '\n')); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(runner.Out, "Identity lease saved to %s\n", leaseDest)
	}
	return nil
}

// RequestPermissions calls POST /permissions to issue or renew a signed
// permissions document (mTLS required).  If output is non-empty the
// permissions_doc_smime field is written to that path; otherwise the full JSON
// response is printed to stdout.
func (runner *Runner) RequestPermissions(url, certFile, keyFile, caFile, serverAddr, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	resp, err := client.Post("/permissions", map[string]any{})
	if err != nil {
		return err
	}
	if output == "" {
		return runner.emitJSON(resp)
	}
	result, err := runner.decodeResult(resp)
	if err != nil {
		return err
	}
	docSMIME, _ := result["permissionsDocSmime"].(string)
	dest := resolveOutputPath(output, "signed_permissions.p7s")
	if err := runner.saveToFile(dest, []byte(docSMIME)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "Permissions document saved to %s\n", dest)
	if leaseData := extractLease(result); leaseData != nil {
		leaseJSON, _ := json.MarshalIndent(leaseData, "", "  ")
		leaseDest := filepath.Join(filepath.Dir(dest), "signed_permissions.lease.json")
		if err := runner.saveToFile(leaseDest, append(leaseJSON, '\n')); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(runner.Out, "Permissions lease saved to %s\n", leaseDest)
	}
	return nil
}

// RequestPSK calls POST /psk to issue or rotate the Provisioning Service PSK
// (mTLS required).  When output is non-empty three files are written to the
// output directory:
//
//	psk_secret.key  — passphrase of the psk_a slot (active seed)
//	psk_secret_extra.key    — psk_a and psk_b passphrases only (max 2 lines; DDS limit)
//	psk_secret.lease.json   — lease windows for both slots + server_time_utc
//
// When output is empty the full JSON response is printed to stdout.
func (runner *Runner) RequestPSK(url, certFile, keyFile, caFile, serverAddr, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	resp, err := client.Post("/psk", map[string]any{})
	if err != nil {
		return err
	}
	if output == "" {
		return runner.emitJSON(resp)
	}
	result, err := runner.decodeResult(resp)
	if err != nil {
		return err
	}

	type pskSlot struct {
		key          string
		passphraseID float64
		passphrase   string
		lease        any
	}
	parseSlot := func(key string) (pskSlot, bool) {
		m, ok := result[key].(map[string]any)
		if !ok {
			return pskSlot{}, false
		}
		id, _ := m["passphraseId"].(float64)
		pass, _ := m["passphrase"].(string)
		return pskSlot{key: key, passphraseID: id, passphrase: pass, lease: m["lease"]}, true
	}
	var slots []pskSlot
	for _, k := range []string{"pskA", "pskB", "psk"} {
		if s, ok := parseSlot(k); ok {
			slots = append(slots, s)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].passphraseID < slots[j].passphraseID })

	// Prefer the explicit psk_a slot as the active primary; fall back to the
	// lowest-id slot for servers that only return the legacy "psk" key.
	primarySlot := slots[0]
	for _, s := range slots {
		if s.key == "pskA" {
			primarySlot = s
			break
		}
	}

	outDir := pskOutputDir(output)

	// psk_secret.key — active passphrase (psk_a, or lowest-id fallback).
	if len(slots) > 0 {
		primaryDest := filepath.Join(outDir, "psk_secret.key")
		if err := runner.saveToFile(primaryDest, []byte(primarySlot.passphrase)); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(runner.Out, "PSK primary passphrase saved to %s\n", primaryDest)
	}

	// psk_secret_extra.key — at most two passphrases: psk_a and psk_b only.
	// DDS supports a maximum of two extra passphrases (one matching the primary
	// and one different); writing more causes a fatal initialisation error.
	var passphrases []string
	for _, s := range slots {
		if s.key == "pskA" || s.key == "pskB" {
			passphrases = append(passphrases, s.passphrase)
		}
	}
	if len(passphrases) == 0 {
		// Legacy server with only a "psk" slot — use it as the sole extra entry.
		for _, s := range slots {
			passphrases = append(passphrases, s.passphrase)
			break
		}
	}
	extraDest := filepath.Join(outDir, "psk_secret_extra.key")
	if err := runner.saveToFile(extraDest, []byte(strings.Join(passphrases, "\n"))); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "PSK extra passphrases saved to %s\n", extraDest)

	// psk_secret.lease.json — lease windows per slot + server_time_utc
	leasePayload := map[string]any{}
	for _, s := range slots {
		if s.lease != nil {
			leasePayload[s.key] = map[string]any{"lease": s.lease}
		}
	}
	if v, ok := result["serverTimeUtc"]; ok {
		leasePayload["serverTimeUtc"] = v
	}
	leaseJSON, _ := json.MarshalIndent(leasePayload, "", "  ")
	leaseDest := filepath.Join(outDir, "psk_secret.lease.json")
	if err := runner.saveToFile(leaseDest, append(leaseJSON, '\n')); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "PSK lease saved to %s\n", leaseDest)
	return nil
}

// RenewDeviceCert calls POST /device/renew-cert to renew the mTLS device
// certificate using the same key pair (mTLS required).  The device presents
// its current certificate via mTLS; the server verifies that the CSR subject
// and public key match the current certificate before signing.
//
// csrFile is a PEM-encoded PKCS#10 CSR generated from the same private key
// currently in use by the device.  validityMinutes, when > 0, requests a
// specific certificate lifetime; the server may cap or ignore the value.
//
// When output is non-empty the response is written as two files:
//
//	<output>/node.crt     — the newly signed certificate
//	<output>/ca-chain.crt   — the CA chain
//
// When output is empty the raw JSON response is printed to stdout.
func (runner *Runner) RenewDeviceCert(url, certFile, keyFile, caFile, serverAddr, csrFile string, validityMinutes int, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	csrData, err := runner.ReadFile(csrFile)
	if err != nil {
		return fmt.Errorf("reading CSR file: %w", err)
	}
	payload := map[string]any{
		"csr": base64.StdEncoding.EncodeToString(csrData),
	}
	if validityMinutes > 0 {
		payload["validity_minutes"] = validityMinutes
	}
	resp, err := client.Post("/device/renew-cert", payload)
	if err != nil {
		return err
	}
	if output == "" {
		return runner.emitJSON(resp)
	}
	result, err := runner.decodeResult(resp)
	if err != nil {
		return err
	}
	certPEM, _ := result["certificate"].(string)
	caDest := resolveOutputPath(output, "ca-chain.crt")
	certDest := resolveOutputPath(output, "node.crt")
	if err := runner.saveToFile(certDest, []byte(certPEM)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "Device certificate saved to %s\n", certDest)
	caChainPEM, _ := result["caChain"].(string)
	if err := runner.saveToFile(caDest, []byte(caChainPEM)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "CA chain saved to %s\n", caDest)
	return nil
}

// GetCRL calls GET /crl to fetch the current Certificate Revocation List
// (mTLS required).  If output is non-empty the CRL is saved to that path
// (parent directory created if needed); otherwise it is printed to stdout.
func (runner *Runner) GetCRL(url, certFile, keyFile, caFile, serverAddr, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	resp, err := client.Get("/crl")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := httputil.FormatError(resp.StatusCode, body)
		_, _ = fmt.Fprintf(runner.Out, "Error: %s\n", msg)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if output == "" {
		_, _ = fmt.Fprintln(runner.Out, string(body))
		return nil
	}
	dest := resolveOutputPath(output, "crl.pem")
	if err := runner.saveToFile(dest, body); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "CRL saved to %s\n", dest)
	return nil
}
