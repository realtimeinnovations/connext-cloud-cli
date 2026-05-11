package cloudapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestWarnsToStderrOnceWhenSSLVerifyDisabled(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var stderr bytes.Buffer
	client := New(
		func() (string, error) { return server.URL, nil },
		func() (map[string]string, error) { return map[string]string{}, nil },
	)
	client.SSLVerify = false
	client.Stderr = &stderr

	response, err := client.Get("/")
	if err != nil {
		t.Fatalf("expected first request to succeed, got error: %v", err)
	}
	response.Body.Close()
	firstClient := client.insecureClient

	response, err = client.Get("/")
	if err != nil {
		t.Fatalf("expected second request to succeed, got error: %v", err)
	}
	response.Body.Close()
	secondClient := client.insecureClient

	warning := "WARNING: SSL certificate verification disabled"
	if count := strings.Count(stderr.String(), warning); count != 1 {
		t.Fatalf("expected warning once on stderr, got %d occurrences in %q", count, stderr.String())
	}
	if firstClient == nil || secondClient == nil || firstClient != secondClient {
		t.Fatalf("expected insecure client to be cached and reused, got %#v and %#v", firstClient, secondClient)
	}
}
