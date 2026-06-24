// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

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
	return client.send(request)
}

func (client *Client) send(request *http.Request) (*http.Response, error) {
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	stopSpinner := terminal.StartSpinner(client.Out, "Connecting to Connext Cloud...")
	defer stopSpinner()
	return httpClient.Do(request)
}

// PostWithBearerToken sends a POST using an explicit Bearer token instead of
// the token produced by HeadersProvider.  Use this when the endpoint requires
// a different JWT audience (e.g. a campaign enrollment token).
// HeadersProvider is intentionally NOT called so that no ambient login flow
// is triggered for requests that carry their own token.
func (client *Client) PostWithBearerToken(path string, payload any, bearerToken string) (*http.Response, error) {
	return client.requestWithExplicitHeaders(http.MethodPost, path, payload, map[string]string{
		"Authorization": "Bearer " + bearerToken,
	})
}

// requestWithExplicitHeaders sends a request using only the provided headers,
// bypassing HeadersProvider entirely.  Use for endpoints that authenticate
// with a caller-supplied token rather than the stored user credentials.
func (client *Client) requestWithExplicitHeaders(method string, path string, payload any, headers map[string]string) (*http.Response, error) {
	baseURL, err := client.BaseURLProvider()
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
	return client.send(request)
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
