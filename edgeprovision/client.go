package edgeprovision

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client communicates with the Edge Provision API sidecar that runs inside
// each EdgeSystem pod.  Device-facing endpoints require mTLS (the device
// presents its client certificate and the server verifies it against the
// EdgeSystem CA).
type Client struct {
	// BaseURL is the Edge Provision API base URL (e.g. "https://alpha.devices.cloud.rti.com:8443").
	BaseURL string
	// HTTPClient is the underlying HTTP client.  When mTLS is needed, it
	// should be configured with a tls.Config that provides the client cert
	// and trusts the EdgeSystem CA.
	HTTPClient *http.Client
	Out        io.Writer
}

// NewClient creates a plain (non-mTLS) client for the signing/healthcheck
// endpoints on port 8080.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewMTLSClient creates a client that presents a client certificate for mTLS
// communication with the device-facing endpoints (port 8443 via nginx).
//
// certFile / keyFile: path to the device's PEM-encoded client certificate and key.
// caFile: path to the EdgeSystem CA chain PEM used to verify the server.
func NewMTLSClient(baseURL string, certFile string, keyFile string, caFile string) (*Client, error) {
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
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	}

	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsConfig},
		},
	}, nil
}

func (c *Client) request(method string, path string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.HTTPClient.Do(req)
}

func (c *Client) Get(path string) (*http.Response, error) {
	return c.request(http.MethodGet, path, nil)
}

func (c *Client) Post(path string, payload any) (*http.Response, error) {
	return c.request(http.MethodPost, path, payload)
}
