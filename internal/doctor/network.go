// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package doctor

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

func probe(ctx context.Context, options Options, endpoint, token, apiKey string) (c Check) {
	c = Check{ID: "cloud_access", Label: "Cloud access", Status: "pass", Message: "Permission to list databuses verified", Details: []Detail{detail("Probe", "GET /databuses")}}
	start := time.Now()
	defer func() { c.DurationMS = time.Since(start).Milliseconds() }()
	client := http.Client{}
	if options.HTTPClient != nil {
		client = *options.HTTPClient
	} else if options.Auth.HTTPClient != nil {
		client = *options.Auth.HTTPClient
	}
	// Even a same-host redirect could forward credentials to an unexpected route.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if apiKey != "" {
		c.Details = []Detail{detail("Probe", "POST /service-accounts/auth/token")}
		manager := *options.Auth
		manager.HTTPClient = &client
		var err error
		token, _, err = manager.GetAccessTokenFromAPIKeyContext(ctx, apiKey, endpoint)
		if err != nil {
			return networkFailure(c, err, "API-key exchange")
		}
		if token == "" {
			c.Status, c.Code, c.Message = "fail", "INVALID_RESPONSE", "API-key exchange returned no access token"
			return c
		}
	}
	tokenSource := "Local token cache"
	if apiKey != "" {
		tokenSource = "API-key exchange · temporary token held in memory"
	}
	c.Details = []Detail{detail("Probe", "GET /databuses"), detail("Token source", tokenSource)}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/databuses", nil)
	if err != nil {
		c.Status, c.Code, c.Message = "fail", "CONFIG_INVALID", "Could not construct the Cloud API request"
		return c
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return networkFailure(c, err, "Cloud API request")
	}
	defer response.Body.Close()
	c.HTTPStatus = response.StatusCode
	if response.StatusCode != http.StatusOK {
		return networkFailure(c, &httputil.StatusError{StatusCode: response.StatusCode}, "Databus list")
	}
	var payload struct {
		Databuses map[string]json.RawMessage `json:"databuses"`
	}
	// Bound both memory and time; discard resource details from the report.
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil {
		return networkFailure(c, err, "Reading Cloud response")
	}
	if len(data) > 8*1024*1024 || json.Unmarshal(data, &payload) != nil || payload.Databuses == nil {
		c.Status, c.Code, c.Message = "fail", "INVALID_RESPONSE", "Cloud response was not a supported databus list"
	}
	return c
}

func networkFailure(c Check, err error, stage string) Check {
	c.Status = "fail"
	c.Code = clierror.From(err).Code
	var status *httputil.StatusError
	var dns *net.DNSError
	var certificate *tls.CertificateVerificationError
	var syntax *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	switch {
	case errors.Is(err, context.Canceled):
		c.Code, c.Message = "CANCELED", stage+" canceled"
	case c.Code == "TIMEOUT":
		c.Message = stage + " timed out; authorization could not be verified"
		c.RequiredAction, c.NextStep = "check_connectivity", "Check network access or increase --timeout, then run rticloud doctor."
	case errors.As(err, &dns):
		c.Code, c.Message = "DNS_ERROR", stage+": DNS resolution failed; authorization could not be verified"
		c.RequiredAction, c.NextStep = "check_connectivity", "Check DNS/network access, then run rticloud doctor."
	case errors.As(err, &certificate):
		c.Code, c.Message = "TLS_ERROR", stage+": TLS certificate verification failed"
		c.RequiredAction, c.NextStep = "check_tls", "Check the endpoint and trusted certificates, then run rticloud doctor."
	case errors.As(err, &syntax), errors.As(err, &typeError), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		c.Code, c.Message = "INVALID_RESPONSE", stage+": incomplete or invalid response"
	case errors.As(err, &status):
		c.HTTPStatus = status.StatusCode
		switch status.StatusCode {
		case 401:
			c.Message = stage + ": credentials rejected (HTTP 401)"
			c.RequiredAction, c.NextStep = "configure_credentials", "Run rticloud login or verify the configured API key, then run rticloud doctor."
		case 403:
			c.Message = stage + ": permission denied (HTTP 403)"
			c.RequiredAction, c.NextStep = "check_permissions", "Verify permissions for the reported operation; a 403 does not establish that the token is expired."
		default:
			c.Message = stage + ": unexpected HTTP response"
			c.RequiredAction, c.NextStep = "check_endpoint", "Check the configured endpoint and service availability, then run rticloud doctor."
		}
	default:
		c.Code, c.Message = "NETWORK_ERROR", stage+" failed; authorization could not be verified"
		c.RequiredAction, c.NextStep = "check_connectivity", "Check network/proxy access and the configured endpoint, then run rticloud doctor."
	}
	// Never echo server bodies, raw transport errors, tokens, or API keys.
	return c
}
