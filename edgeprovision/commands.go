package edgeprovision

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

// Runner executes edge provision commands against the Edge Provision API.
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
	NewMTLSClient func(baseURL, certFile, keyFile, caFile string, sslVerify bool) (*Client, error)
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
// of cert/key/ca must be provided together.
func (runner *Runner) mtlsClient(url, certFile, keyFile, caFile string) (*Client, error) {
	if url == "" {
		return nil, fmt.Errorf("--url is required")
	}
	if certFile == "" || keyFile == "" || caFile == "" {
		return nil, fmt.Errorf("--cert, --key, and --ca are required for mTLS endpoints")
	}
	return runner.NewMTLSClient(url, certFile, keyFile, caFile, runner.SSLVerify)
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
func (runner *Runner) DeviceStatus(url, certFile, keyFile, caFile string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile)
	if err != nil {
		return err
	}
	resp, err := client.Get("/device/status")
	if err != nil {
		return err
	}
	return runner.emitJSON(resp)
}

// RequestIdentity calls POST /{participantID}/identity to issue or renew
// an identity certificate (mTLS required).
func (runner *Runner) RequestIdentity(url, certFile, keyFile, caFile, participantID, csrFile string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile)
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
	return runner.emitJSON(resp)
}

// RequestPermissions calls POST /{participantID}/permissions to issue or renew
// a signed permissions document (mTLS required).
func (runner *Runner) RequestPermissions(url, certFile, keyFile, caFile, participantID string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile)
	if err != nil {
		return err
	}
	resp, err := client.Post("/"+participantID+"/permissions", map[string]any{})
	if err != nil {
		return err
	}
	return runner.emitJSON(resp)
}

// RequestPSK calls POST /psk to issue or rotate the EdgeSystem PSK (mTLS required).
func (runner *Runner) RequestPSK(url, certFile, keyFile, caFile string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile)
	if err != nil {
		return err
	}
	resp, err := client.Post("/psk", map[string]any{})
	if err != nil {
		return err
	}
	return runner.emitJSON(resp)
}

// GetCRL calls GET /{participantID}/crl to fetch the current Certificate
// Revocation List (mTLS required).  If output is non-empty the CRL is saved
// to that path (parent directory created if needed); otherwise it is printed
// to stdout.
func (runner *Runner) GetCRL(url, certFile, keyFile, caFile, participantID, output string) error {
	client, err := runner.mtlsClient(url, certFile, keyFile, caFile)
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
	if dir := filepath.Dir(output); dir != "." {
		if err := runner.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := runner.WriteFile(output, body, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "CRL saved to %s\n", output)
	return nil
}
