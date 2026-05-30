package cloudapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
)

type BaseURLProvider func() (string, error)
type HeadersProvider func() (map[string]string, error)

type Client struct {
	BaseURLProvider BaseURLProvider
	HeadersProvider HeadersProvider
	HTTPClient      *http.Client
	Out             io.Writer
}

func New(baseURLProvider BaseURLProvider, headersProvider HeadersProvider) *Client {
	return &Client{
		BaseURLProvider: baseURLProvider,
		HeadersProvider: headersProvider,
		HTTPClient:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (client *Client) Request(method string, path string, payload any) (*http.Response, error) {
	baseURL, err := client.BaseURLProvider()
	if err != nil {
		return nil, err
	}
	headers, err := client.HeadersProvider()
	if err != nil {
		return nil, err
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
	stopSpinner := terminal.StartSpinner(client.Out, "Connecting to Connext Cloud...")
	defer stopSpinner()
	return httpClient.Do(request)
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
