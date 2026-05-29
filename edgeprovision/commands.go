package edgeprovision

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

// Runner executes agent commands against the Provisioning Service API.
// One Runner is created at startup and shared across subcommands; each
// command builds its own Client on demand (mTLS or plain) because cert/key/ca
// and base URL come from the user on the command line.
type Runner struct {
	Out       io.Writer
	SSLVerify bool

	// Injectable file I/O for testability — mirrors commands.Runner.
	ReadFile  func(string) ([]byte, error)
	WriteFile func(string, []byte, os.FileMode) error
	MkdirAll  func(string, os.FileMode) error

	// Client factories — overridable in tests with a fake Doer.
	NewClient     func(baseURL string, sslVerify bool) *Client
	NewMTLSClient func(baseURL, certFile, keyFile, caFile, serverAddr string, sslVerify bool) (*Client, error)
}

// NewRunner creates a Runner with sensible defaults.  SSLVerify defaults to
// true; the parser flips it via PersistentPreRunE when --disable-ssl-verify
// is passed.
func NewRunner(out io.Writer) *Runner {
	return &Runner{
		Out:           out,
		SSLVerify:     true,
		ReadFile:      os.ReadFile,
		WriteFile:     os.WriteFile,
		MkdirAll:      os.MkdirAll,
		NewClient:     NewClient,
		NewMTLSClient: NewMTLSClient,
	}
}

// plainClient builds a non-mTLS client (used for /healthz and /internal/sign).
func (runner *Runner) plainClient(url string) (*Client, error) {
	if url == "" {
		return nil, fmt.Errorf("--url is required")
	}
	return runner.NewClient(url, runner.SSLVerify), nil
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
	return runner.NewMTLSClient(url, certFile, keyFile, caFile, serverAddr, runner.SSLVerify)
}

// emitJSON consumes resp, prints either the formatted JSON body on success or
// a normalized "Error: …" line on failure.  Matches the behaviour of
// commands.Runner.printResponseError so users see a consistent error style.
func (runner *Runner) emitJSON(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Error: %s\n", httputil.FormatError(resp.StatusCode, body))
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

// Healthz calls GET /healthz on the signing app (port 8080).
func (runner *Runner) Healthz(url string) error {
	client, err := runner.plainClient(url)
	if err != nil {
		return err
	}
	resp, err := client.Get("/healthz")
	if err != nil {
		return err
	}
	return runner.emitJSON(resp)
}

// SignCSR calls POST /internal/sign with a base64-encoded CSR.
func (runner *Runner) SignCSR(url string, csrBase64 string) error {
	client, err := runner.plainClient(url)
	if err != nil {
		return err
	}
	resp, err := client.Post("/internal/sign", map[string]any{"csr": csrBase64})
	if err != nil {
		return err
	}
	return runner.emitJSON(resp)
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

// RequestIdentity calls POST /{participantID}/identity to issue or renew
// an identity certificate (mTLS required).  If output is non-empty the
// identity_cert_pem field is written to that path; otherwise the full JSON
// response is printed to stdout.
func (runner *Runner) RequestIdentity(url, certFile, keyFile, caFile, serverAddr, participantID, csrFile, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	payload := map[string]any{}
	if csrFile != "" {
		data, err := runner.ReadFile(csrFile)
		if err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Error reading CSR file: %v\n", err)
			return nil
		}
		payload["csr_pem"] = string(data)
	}
	resp, err := client.Post("/"+participantID+"/identity", payload)
	if err != nil {
		return err
	}
	if output == "" {
		return runner.emitJSON(resp)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Error: %s\n", httputil.FormatError(resp.StatusCode, body))
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	certPEM, _ := result["identity_cert_pem"].(string)
	dest := resolveOutputPath(output, "identity.crt")
	if err := runner.saveToFile(dest, []byte(certPEM)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "Identity certificate saved to %s\n", dest)
	return nil
}

// RequestPermissions calls POST /{participantID}/permissions to issue or renew
// a signed permissions document (mTLS required).  If output is non-empty the
// permissions_doc_smime field is written to that path; otherwise the full JSON
// response is printed to stdout.
func (runner *Runner) RequestPermissions(url, certFile, keyFile, caFile, serverAddr, participantID, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	resp, err := client.Post("/"+participantID+"/permissions", map[string]any{})
	if err != nil {
		return err
	}
	if output == "" {
		return runner.emitJSON(resp)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Error: %s\n", httputil.FormatError(resp.StatusCode, body))
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	docSMIME, _ := result["permissions_doc_smime"].(string)
	dest := resolveOutputPath(output, "signed_permissions.p7s")
	if err := runner.saveToFile(dest, []byte(docSMIME)); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "Permissions document saved to %s\n", dest)
	return nil
}

// RequestPSK calls POST /psk to issue or rotate the Provisioning Service PSK (mTLS
// required).  If output is non-empty the full JSON response is written to
// that path; otherwise it is printed to stdout.
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
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Error: %s\n", httputil.FormatError(resp.StatusCode, body))
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	dest := resolveOutputPath(output, "psk.json")
	if err := runner.saveToFile(dest, append(formatted, '\n')); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "PSK saved to %s\n", dest)
	return nil
}

// GetCRL calls GET /{participantID}/crl to fetch the current Certificate
// Revocation List (mTLS required).  If output is non-empty the CRL is saved
// to that path (parent directory created if needed); otherwise it is printed
// to stdout.
func (runner *Runner) GetCRL(url, certFile, keyFile, caFile, serverAddr, participantID, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile, serverAddr)
	if err != nil {
		return err
	}
	resp, err := client.Get("/" + participantID + "/crl")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Error: %s\n", httputil.FormatError(resp.StatusCode, body))
		return nil
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
