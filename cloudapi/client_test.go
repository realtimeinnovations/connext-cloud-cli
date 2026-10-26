// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cloudapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestRequestPreservesHTTPContract(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			var payload any
			if method == http.MethodPost || method == http.MethodPatch {
				payload = map[string]any{"name": "demo/one", "secure": true}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != method || r.RequestURI != "/api/v1/databuses/demo%2Fone?extra_fields=true" {
					t.Errorf("request = %s %s", r.Method, r.RequestURI)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer stored-token" {
					t.Errorf("authorization = %q", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if payload == nil {
					if len(body) != 0 || r.Header.Get("Content-Type") != "" {
						t.Errorf("bodyless request has body %q or content type %q", body, r.Header.Get("Content-Type"))
					}
				} else {
					var got any
					if err := json.Unmarshal(body, &got); err != nil || !reflect.DeepEqual(got, payload) || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("JSON request = %q, content type %q, error %v", body, r.Header.Get("Content-Type"), err)
					}
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"already exists"}`)
			}))
			defer server.Close()
			client := New(func() (string, error) { return server.URL + "/api/v1/", nil }, func() (map[string]string, error) {
				return map[string]string{"Authorization": "Bearer stored-token"}, nil
			})
			response, err := client.Request(method, "/databuses/demo%2Fone?extra_fields=true", payload)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusConflict || string(body) != `{"error":"already exists"}` || calls.Load() != 1 {
				t.Fatalf("response = %d %q, calls=%d, error=%v", response.StatusCode, body, calls.Load(), err)
			}
		})
	}
}

func TestCampaignBearerBypassesStoredCredentials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.RequestURI != "/edge-systems/service/enroll" {
			t.Errorf("request = %s %s", r.Method, r.RequestURI)
		}
		if r.Header.Get("Authorization") != "Bearer campaign-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected headers: %v", r.Header)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || !reflect.DeepEqual(payload, map[string]string{"serial": "SN-1"}) {
			t.Errorf("payload = %v, error=%v", payload, err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	client := New(func() (string, error) { return server.URL, nil }, func() (map[string]string, error) {
		t.Fatal("explicit campaign token must not read stored credentials or start login")
		return nil, nil
	})
	response, err := client.PostWithBearerToken("/edge-systems/service/enroll", map[string]string{"serial": "SN-1"}, "campaign-token")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d", response.StatusCode, calls.Load())
	}
}

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
