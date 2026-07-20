// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

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

// defaultTransport returns a private copy of the default transport, so tuning
// TLS or dialing on it cannot leak into other callers.  http.DefaultTransport
// is a var any package may replace, so the assertion is checked rather than
// left to panic.
func defaultTransport() *http.Transport {
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		return base.Clone()
	}
	return &http.Transport{Proxy: http.ProxyFromEnvironment}
}

// NewClient creates a plain (non-mTLS) client for the signing/healthcheck
// endpoints on port 8080.  All Provisioning Service endpoints use TLS with
// certificate verification, which is always enabled.
func NewClient(baseURL string) *Client {
	transport := defaultTransport()
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

// NewClientWithCA creates a plain (non-mTLS) client that trusts the given CA
// chain PEM file for server certificate verification.
func NewClientWithCA(baseURL string, caFile string) (*Client, error) {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA file: %w", err)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate from %s", caFile)
	}
	transport := defaultTransport()
	transport.TLSClientConfig = &tls.Config{
		RootCAs: caCertPool,
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
func NewMTLSClient(baseURL string, certFile string, keyFile string, caFile string, serverAddr string) (*Client, error) {
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

	transport := defaultTransport()
	transport.TLSClientConfig = tlsConfig
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
