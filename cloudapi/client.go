package cloudapi

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
)

type BaseURLProvider func() (string, error)
type HeadersProvider func() (map[string]string, error)

type Client struct {
	BaseURLProvider BaseURLProvider
	HeadersProvider HeadersProvider
	HTTPClient      *http.Client
	SSLVerify       bool
	Stderr          io.Writer
	Out             io.Writer
	warningOnce     sync.Once
	insecureOnce    sync.Once
	insecureClient  *http.Client
}

func New(baseURLProvider BaseURLProvider, headersProvider HeadersProvider) *Client {
	return &Client{
		BaseURLProvider: baseURLProvider,
		HeadersProvider: headersProvider,
		HTTPClient:      &http.Client{Timeout: 30 * time.Second},
		SSLVerify:       true,
		Stderr:          os.Stderr,
	}
}

func (client *Client) Request(method string, path string, payload any) (*http.Response, error) {
	return client.requestWithHeaders(method, path, payload, nil)
}

// requestWithHeaders is the common implementation.  extraHeaders, if non-nil,
// override any matching key produced by HeadersProvider.
func (client *Client) requestWithHeaders(method string, path string, payload any, extraHeaders map[string]string) (*http.Response, error) {
	baseURL, err := client.BaseURLProvider()
	if err != nil {
		return nil, err
	}
	headers, err := client.HeadersProvider()
	if err != nil {
		return nil, err
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	if !client.SSLVerify {
		client.warningOnce.Do(func() {
			if client.Stderr != nil {
				_, _ = fmt.Fprintln(client.Stderr, "WARNING: SSL certificate verification disabled")
			}
		})
		client.insecureOnce.Do(func() {
			transport := insecureTransport(httpClient.Transport)
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			insecureClient := *httpClient
			insecureClient.Transport = transport
			if insecureClient.Timeout == 0 {
				insecureClient.Timeout = 30 * time.Second
			}
			client.insecureClient = &insecureClient
		})
		httpClient = client.insecureClient
	}
	stopSpinner := terminal.StartSpinner(client.Out, "Connecting to Connext Cloud...")
	defer stopSpinner()
	return httpClient.Do(request)
}

// PostWithBearerToken sends a POST using an explicit Bearer token instead of
// the token produced by HeadersProvider.  Use this when the endpoint requires
// a different JWT audience (e.g. a campaign enrollment token).
func (client *Client) PostWithBearerToken(path string, payload any, bearerToken string) (*http.Response, error) {
	return client.requestWithHeaders(http.MethodPost, path, payload, map[string]string{
		"Authorization": "Bearer " + bearerToken,
	})
}

func insecureTransport(roundTripper http.RoundTripper) *http.Transport {
	if transport, ok := roundTripper.(*http.Transport); ok && transport != nil {
		return transport.Clone()
	}
	return http.DefaultTransport.(*http.Transport).Clone()
}

func (client *Client) Get(path string) (*http.Response, error) {
	return client.Request(http.MethodGet, path, nil)
}

func (client *Client) Post(path string, payload any) (*http.Response, error) {
	return client.Request(http.MethodPost, path, payload)
}

func (client *Client) Patch(path string, payload any) (*http.Response, error) {
	return client.Request(http.MethodPatch, path, payload)
}

func (client *Client) Delete(path string) (*http.Response, error) {
	return client.Request(http.MethodDelete, path, nil)
}
