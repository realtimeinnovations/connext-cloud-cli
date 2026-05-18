package edgeprovision

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Runner executes edge provision commands against the Edge Provision API.
type Runner struct {
	Client   *Client
	Out      io.Writer
	ReadFile func(string) ([]byte, error)
}

// NewRunner creates a Runner with sensible defaults.
func NewRunner(client *Client, out io.Writer) *Runner {
	return &Runner{
		Client:   client,
		Out:      out,
		ReadFile: os.ReadFile,
	}
}

func (r *Runner) printError(prefix string, statusCode int, body []byte) {
	_, _ = fmt.Fprintf(r.Out, "%s(HTTP %d) %s\n", prefix, statusCode, string(body))
}

// Healthz calls GET /healthz on the signing app (port 8080).
func (r *Runner) Healthz() error {
	resp, err := r.Client.Get("/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		r.printError("Error: ", resp.StatusCode, body)
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(r.Out, string(formatted))
	return nil
}

// SignCSR calls POST /internal/sign with a base64-encoded CSR.
func (r *Runner) SignCSR(csrBase64 string) error {
	payload := map[string]any{"csr": csrBase64}
	resp, err := r.Client.Post("/internal/sign", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		r.printError("Error: ", resp.StatusCode, body)
		return nil
	}
	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(r.Out, string(formatted))
	return nil
}

// DeviceStatus calls GET /device/status (mTLS required).
func (r *Runner) DeviceStatus() error {
	resp, err := r.Client.Get("/device/status")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		r.printError("Error: ", resp.StatusCode, body)
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(r.Out, string(formatted))
	return nil
}

// RequestIdentity calls POST /{participantID}/identity to issue or renew
// an identity certificate (mTLS required).
func (r *Runner) RequestIdentity(participantID string, csrFile string) error {
	payload := map[string]any{}
	if csrFile != "" {
		data, err := r.ReadFile(csrFile)
		if err != nil {
			_, _ = fmt.Fprintf(r.Out, "Error reading CSR file: %v\n", err)
			return nil
		}
		payload["csr_pem"] = string(data)
	}
	resp, err := r.Client.Post("/"+participantID+"/identity", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		r.printError("Error: ", resp.StatusCode, body)
		return nil
	}
	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(r.Out, string(formatted))
	return nil
}

// RequestPermissions calls POST /{participantID}/permissions to issue or renew
// a signed permissions document (mTLS required).
func (r *Runner) RequestPermissions(participantID string) error {
	resp, err := r.Client.Post("/"+participantID+"/permissions", map[string]any{})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		r.printError("Error: ", resp.StatusCode, body)
		return nil
	}
	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(r.Out, string(formatted))
	return nil
}

// RequestPSK calls POST /psk to issue or rotate the EdgeSystem PSK (mTLS required).
func (r *Runner) RequestPSK() error {
	resp, err := r.Client.Post("/psk", map[string]any{})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		r.printError("Error: ", resp.StatusCode, body)
		return nil
	}
	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(r.Out, string(formatted))
	return nil
}

// GetCRL calls GET /{participantID}/crl to fetch the current Certificate
// Revocation List (mTLS required).
func (r *Runner) GetCRL(participantID string, output string) error {
	resp, err := r.Client.Get("/" + participantID + "/crl")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		r.printError("Error: ", resp.StatusCode, body)
		return nil
	}
	if output == "" {
		_, _ = fmt.Fprintln(r.Out, string(body))
		return nil
	}
	if err := os.WriteFile(output, body, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(r.Out, "CRL saved to %s\n", output)
	return nil
}
