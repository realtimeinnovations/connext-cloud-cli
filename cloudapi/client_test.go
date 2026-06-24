// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cloudapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestFailsForSelfSignedTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := New(
		func() (string, error) { return server.URL, nil },
		func() (map[string]string, error) { return map[string]string{}, nil },
	)
	_, err := client.Get("/")
	if err == nil {
		t.Fatal("expected TLS verification error for self-signed server")
	}
}
