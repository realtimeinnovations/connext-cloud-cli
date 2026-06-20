package edgeprovision

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Doer is the http.Client method that Client uses for transport.  Tests
// substitute their own Doer to avoid spinning up real HTTP servers.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client communicates with the Edge Provision API sidecar that runs inside
// each Provisioning Service pod.  Device-facing endpoints require mTLS (the device
// presents its client certificate and the server verifies it against the
// Provisioning Service CA).
type Client struct {
	// BaseURL is the Edge Provision API base URL (e.g. "https://alpha.devices.cloud.rti.com:8443").
	BaseURL string
	// HTTPClient is the underlying HTTP doer.  When mTLS is needed, it is
	// configured with a tls.Config that provides the client cert and trusts
	// the Provisioning Service CA.
	HTTPClient Doer
	// DebugOut, when non-nil, receives verbose request/response logging for
	// every HTTP call.  Sensitive fields (keys, passphrases) are not redacted.
	DebugOut io.Writer
}

// NewClient creates a plain (non-mTLS) client for the signing/healthcheck
// endpoints on port 8080.  When sslVerify is false the underlying transport
// is configured with InsecureSkipVerify.
func NewClient(baseURL string, sslVerify bool) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if !sslVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// NewClientWithCA creates a plain (non-mTLS) client that trusts the given CA
// chain PEM file for server certificate verification.
func NewClientWithCA(baseURL string, caFile string, sslVerify bool) (*Client, error) {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", caFile)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		RootCAs:            caCertPool,
		InsecureSkipVerify: !sslVerify,
	}
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}, nil
}

// NewMTLSClient creates a client that presents a client certificate for mTLS
// communication with the device-facing endpoints (port 8443 via nginx).
//
// certFile / keyFile: path to the device's PEM-encoded client certificate and key.
// caFile: path to the Provisioning Service CA chain PEM used to verify the server.
// serverAddr: optional "host:port" to connect to at the TCP level (equivalent to
// curl's --connect-to).  When non-empty the TLS dial target is overridden to
// serverAddr while the TLS SNI / certificate verification use the URL hostname.
// Use this to route through an NLB whose DNS is not yet globally resolvable.
func NewMTLSClient(baseURL string, certFile string, keyFile string, caFile string, serverAddr string, sslVerify bool) (*Client, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading client certificate: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", caFile)
	}

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: !sslVerify,
	}

	transport := &http.Transport{TLSClientConfig: tlsConfig}
	if serverAddr != "" {
		// Redirect TCP dial to serverAddr while keeping TLS SNI from the URL hostname.
		// Equivalent to curl's --connect-to flag.
		dialer := &net.Dialer{Timeout: 30 * time.Second}
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, serverAddr)
		}
	}

	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		},
	}, nil
}

func (client *Client) request(method string, path string, payload any) (*http.Response, error) {
	var bodyBytes []byte
	var body io.Reader
	if payload != nil {
		var err error
		bodyBytes, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequest(method, client.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client.DebugOut != nil {
		_, _ = fmt.Fprintf(client.DebugOut, "DEBUG request: %s %s\n", method, client.BaseURL+path)
		if bodyBytes != nil {
			_, _ = fmt.Fprintf(client.DebugOut, "DEBUG request body: %s\n", string(bodyBytes))
		}
	}
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if client.DebugOut != nil {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		_, _ = fmt.Fprintf(client.DebugOut, "DEBUG response: %d %s\n", resp.StatusCode, resp.Status)
		_, _ = fmt.Fprintf(client.DebugOut, "DEBUG response body: %s\n", string(respBody))
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
	}
	return resp, nil
}

func (client *Client) Get(path string) (*http.Response, error) {
	return client.request(http.MethodGet, path, nil)
}

func (client *Client) Post(path string, payload any) (*http.Response, error) {
	return client.request(http.MethodPost, path, payload)
}
