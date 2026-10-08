// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package doctor

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/rtipaths"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
}
func fixture(t *testing.T) (Options, string, connext.Install) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "LocalAppData"))
	oldHome, oldPlatform, oldPaths := connext.UserHomeDir, connext.Platform, rtipaths.UserHomeDir
	connext.UserHomeDir = func() (string, error) { return home, nil }
	connext.Platform = func() (string, string) { return "linux", "amd64" }
	rtipaths.UserHomeDir = connext.UserHomeDir
	t.Cleanup(func() { connext.UserHomeDir, connext.Platform, rtipaths.UserHomeDir = oldHome, oldPlatform, oldPaths })
	t.Setenv("NDDSHOME", "")
	t.Setenv("RTI_LICENSE_FILE", "")
	t.Setenv("PATH", "")
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.FixedZone("PDT", -7*3600))
	cfg := config.New("")
	writeFile(t, cfg.Path, `{"api_host":"https://cloud.example.test/api/v1"}`)
	manager := auth.New(cfg, "")
	manager.Env = func(string) string { return "" }
	manager.OpenBrowser = func(string) error { t.Fatal("browser opened"); return nil }
	writeFile(t, manager.TokenPath, `{"access_token":"SECRET_SAVED","expires_at":"2026-10-04T20:00:00Z"}`)
	path, err := connext.ManagedInstallationPath()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(path, "rti_versions.xml"), `<rti><host><base_version>7.7.0.1</base_version><install_dirname>rti_connext_dds-7.7.0</install_dirname></host></rti>`)
	for _, tool := range []string{"rtiroutingservice", "rticollectorservicelite", "rtiddsspy"} {
		writeFile(t, connext.Executable(path, tool), "launcher")
	}
	license := "FEATURE RTIPRO RTI 4. 12-oct-2026 0 SIGN=SECRET_LICENSE\nFEATURE RTISECURITY RTI 5. 12-oct-2026 0 SIGN=SECRET_LICENSE"
	writeFile(t, filepath.Join(home, ".rti", "rticloud", "licenses", "rti_license.dat"), license)
	writeFile(t, filepath.Join(path, "rti_license.dat"), license)
	project := filepath.Join(home, "project")
	gatewayPath, spyPath := filepath.Join(project, ".connext", "gateway.yaml"), filepath.Join(project, ".connext", "spy.yaml")
	writeFile(t, gatewayPath, "runtime:\n  min_version: 7.3.0\n")
	writeFile(t, spyPath, "runtime:\n  min_version: 7.7.0\n")
	options := Options{Config: cfg, Auth: manager, Now: now, GatewayPath: gatewayPath, SpyPath: spyPath, HTTPClient: &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/databuses" || r.Header.Get("Authorization") != "Bearer SECRET_SAVED" {
			t.Fatalf("unexpected probe: %s %s", r.Method, r.URL.Path)
		}
		return response(200, `{"databuses":{}}`), nil
	})}}
	return options, home, connext.Install{Path: path, Version: "7.7.0.1"}
}
func check(t *testing.T, r Report, id string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("missing check %s", id)
	return Check{}
}
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func assertNoSecrets(t *testing.T, r Report) {
	t.Helper()
	for _, format := range []string{"text", "json"} {
		var b bytes.Buffer
		if err := r.Write(&b, format); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "SECRET_") {
			t.Fatal("credential or license material leaked")
		}
	}
}

func TestDoctorHealthyReadOnlyAndLicenseDays(t *testing.T) {
	o, home, _ := fixture(t)
	// Even pending legacy files must not be migrated by inspection.
	writeFile(t, filepath.Join(home, ".rticloud", "config.json"), `{"legacy":true}`)
	before := snapshot(t, home)
	o.Auth.Env = func(string) string { return "SECRET_API_KEY" }
	r := Run(context.Background(), o)
	if r.ExitCode != 0 || !r.Healthy {
		t.Fatalf("%+v", r)
	}
	if c := check(t, r, "license"); c.Status != "warn" || len(c.Features) != 2 || *c.Features[0].DaysRemaining != 9 {
		t.Fatalf("%+v", c)
	}
	if c := check(t, r, "cloud_access"); c.HTTPStatus != 200 || c.Status != "pass" {
		t.Fatal(c)
	}
	if !reflect.DeepEqual(before, snapshot(t, home)) {
		t.Fatal("inspection changed files")
	}
	assertNoSecrets(t, r)
}

func TestCredentialsFailureAndAPIKeyFallbackDoNotMutate(t *testing.T) {
	for _, state := range []string{"expired", "malformed", "missing", "unreadable"} {
		for _, key := range []bool{false, true} {
			t.Run(state+"/"+map[bool]string{false: "saved", true: "key"}[key], func(t *testing.T) {
				o, home, _ := fixture(t)
				switch state {
				case "expired":
					writeFile(t, o.Auth.TokenPath, `{"access_token":"SECRET_OLD","expires_at":"2020-01-01T00:00:00Z"}`)
				case "malformed":
					writeFile(t, o.Auth.TokenPath, `{"access_token":"SECRET_BAD"`)
				case "missing":
					if err := os.Remove(o.Auth.TokenPath); err != nil {
						t.Fatal(err)
					}
				case "unreadable":
					os.Remove(o.Auth.TokenPath)
					os.Mkdir(o.Auth.TokenPath, 0700)
				}
				if key {
					o.Auth.Env = func(string) string { return "SECRET_API_KEY" }
				}
				calls := 0
				o.HTTPClient = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if calls == 1 {
						if req.Method != "POST" || req.URL.Path != "/api/v1/service-accounts/auth/token" || req.Header.Get("X-API-Key") != "SECRET_API_KEY" {
							t.Fatal("wrong key exchange")
						}
						return response(200, `{"access_token":"SECRET_EXCHANGED","expires_in":300}`), nil
					}
					if req.Header.Get("Authorization") != "Bearer SECRET_EXCHANGED" {
						t.Fatal("wrong token")
					}
					return response(200, `{"databuses":{}}`), nil
				})}
				before := snapshot(t, home)
				r := Run(context.Background(), o)
				if key && state != "unreadable" {
					if r.ExitCode != 0 || calls != 2 {
						t.Fatalf("%+v calls %d", r, calls)
					}
				} else {
					if r.ExitCode == 0 || calls != 0 || check(t, r, "cloud_access").Status != "skip" {
						t.Fatalf("%+v calls %d", r, calls)
					}
				}
				if !reflect.DeepEqual(before, snapshot(t, home)) {
					t.Fatal("doctor mutated credentials")
				}
				assertNoSecrets(t, r)
			})
		}
	}
}

func TestCloudFailureAttributionAndRedaction(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		err    error
		code   string
		exit   int
	}{
		{"dns", 0, "", &net.DNSError{Err: "SECRET_DNS", IsNotFound: true}, "DNS_ERROR", 1},
		{"tls", 0, "", &tls.CertificateVerificationError{Err: errors.New("SECRET_CERT")}, "TLS_ERROR", 1},
		{"timeout", 0, "", context.DeadlineExceeded, "TIMEOUT", 6},
		{"cancel", 0, "", context.Canceled, "CANCELED", 130},
		{"401", 401, "SECRET_BODY", nil, "AUTH_REQUIRED", 3},
		{"403", 403, "SECRET_BODY", nil, "PERMISSION_DENIED", 3},
		{"500", 500, "SECRET_BODY", nil, "API_ERROR", 1},
		{"invalid", 200, `{"secret":"SECRET_BODY"}`, nil, "INVALID_RESPONSE", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _ := fixture(t)
			o.HTTPClient = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return response(tc.status, tc.body), nil
			})}
			r := Run(context.Background(), o)
			c := check(t, r, "cloud_access")
			if r.ExitCode != tc.exit || c.Code != tc.code {
				t.Fatalf("%+v exit %d", c, r.ExitCode)
			}
			if check(t, r, "credentials").Status != "pass" {
				t.Fatal("server error changed local credential finding")
			}
			if check(t, r, "spy_tool").Status != "pass" {
				t.Fatal("network failure suppressed independent local checks")
			}
			assertNoSecrets(t, r)
		})
	}
}

func TestNetworkBudgetSharedWithKeyExchangeAndRedirectsBlocked(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		o, _, _ := fixture(t)
		os.Remove(o.Auth.TokenPath)
		o.Auth.Env = func(string) string { return "SECRET_API_KEY" }
		o.Timeout = 30 * time.Millisecond
		var deadline time.Time
		calls := 0
		o.HTTPClient = &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			d, ok := req.Context().Deadline()
			if !ok {
				t.Fatal("unbounded request")
			}
			if calls == 1 {
				deadline = d
				return response(200, `{"access_token":"SECRET_EXCHANGED"}`), nil
			}
			if d != deadline {
				t.Fatal("timeout restarted")
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}
		r := Run(context.Background(), o)
		if r.ExitCode != 6 {
			t.Fatal(r)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		o, _, _ := fixture(t)
		calls := 0
		o.HTTPClient = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			calls++
			res := response(302, "")
			res.Header.Set("Location", "https://other.example.test/collect")
			return res, nil
		})}
		r := Run(context.Background(), o)
		if calls != 1 || check(t, r, "cloud_access").HTTPStatus != 302 {
			t.Fatal("redirect was followed")
		}
	})
}

func TestInvalidConfigurationDoesNotProbeOrExposeURLSecrets(t *testing.T) {
	o, _, _ := fixture(t)
	writeFile(t, o.Config.Path, `{"api_host":"https://user:SECRET_PASSWORD@host/api?key=SECRET_QUERY"}`)
	o.HTTPClient = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network"); return nil, nil })}
	r := Run(context.Background(), o)
	if r.ExitCode != 1 || check(t, r, "cloud_access").Status != "skip" {
		t.Fatal(r)
	}
	assertNoSecrets(t, r)
}

func TestLocalFailuresRemainScoped(t *testing.T) {
	o, _, install := fixture(t)
	os.Remove(connext.Executable(install.Path, "rtiddsspy"))
	writeFile(t, o.GatewayPath, "[invalid: yaml")
	os.Remove(o.SpyPath)
	r := Run(context.Background(), o)
	if check(t, r, "managed_connext").Status != "pass" || check(t, r, "gateway_tools").Status != "pass" || check(t, r, "spy_tool").Status != "fail" || check(t, r, "gateway_config").Status != "fail" || check(t, r, "spy_config").Status != "skip" {
		t.Fatal(r)
	}
	if r.ExitCode != 1 {
		t.Fatal(r)
	}
	if c := check(t, r, "spy_config"); c.NextStep != "" || c.RequiredAction != "" {
		t.Fatal("unused Spy project generated an action")
	}
	if c := check(t, r, "gateway_config"); c.NextStep == "" || c.RequiredAction == "" {
		t.Fatal("broken existing Gateway project needs an action")
	}
}

func TestMissingAndIncompleteInstallation(t *testing.T) {
	for _, marker := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "incomplete"}[marker], func(t *testing.T) {
			o, _, install := fixture(t)
			os.RemoveAll(install.Path)
			if marker {
				writeFile(t, filepath.Join(filepath.Dir(install.Path), ".rticloud-installing"), "pending")
			}
			r := Run(context.Background(), o)
			want := CheckWarn
			if marker {
				want = CheckFail
			}
			if check(t, r, "managed_connext").Status != want || check(t, r, "gateway_tools").Status != "skip" {
				t.Fatal(r)
			}
		})
	}
}

func TestJSONReportHasOneDocument(t *testing.T) {
	o, _, _ := fixture(t)
	r := Run(context.Background(), o)
	var b bytes.Buffer
	r.Write(&b, "json")
	decoder := json.NewDecoder(&b)
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	if value["schema_version"] != "1" {
		t.Fatal(value)
	}
	if err := decoder.Decode(&value); err != io.EOF {
		t.Fatal("extra output")
	}
}

func TestPathScanDeduplicatesLinks(t *testing.T) {
	root := t.TempDir()
	name := "rticloud"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	active := filepath.Join(root, "active", name)
	alias := filepath.Join(root, "alias", name)
	other := filepath.Join(root, "other", name)
	writeFile(t, active, "one")
	writeFile(t, other, "two")
	os.MkdirAll(filepath.Dir(alias), 0700)
	if err := os.Symlink(active, alias); err != nil {
		t.Skip(err)
	}
	result := pathCopies(active, strings.Join([]string{filepath.Dir(active), filepath.Dir(alias), filepath.Dir(other), filepath.Dir(other)}, string(os.PathListSeparator)))
	if len(result) != 1 || result[0] != other {
		t.Fatal(result)
	}
}

func TestLicenseInspectionDoesNotProvisionFallback(t *testing.T) {
	o, home, install := fixture(t)
	canonical := filepath.Join(home, ".rti", "rticloud", "licenses", "rti_license.dat")
	os.Remove(canonical)
	os.Remove(filepath.Join(install.Path, "rti_license.dat"))
	source := filepath.Join(home, "external-license.dat")
	writeFile(t, source, "FEATURE RTIPRO RTI 4. 12-oct-2026 SECRET_LICENSE")
	t.Setenv("RTI_LICENSE_FILE", source)
	before := snapshot(t, home)
	r := Run(context.Background(), o)
	c := check(t, r, "license")
	if len(c.Features) != 1 || *c.Features[0].DaysRemaining != 9 || c.Status != "warn" {
		t.Fatalf("%+v", c)
	}
	if !reflect.DeepEqual(before, snapshot(t, home)) {
		t.Fatal("license was provisioned")
	}
	if c.Details[0].Value != source {
		t.Fatal("wrong license provenance")
	}
}

func TestUnsupportedManagedPlatformIsWarning(t *testing.T) {
	o, _, _ := fixture(t)
	connext.Platform = func() (string, string) { return "darwin", "amd64" }
	r := Run(context.Background(), o)
	if r.ExitCode != 0 || check(t, r, "managed_connext").Code != "MANAGED_UNAVAILABLE" {
		t.Fatal(r)
	}
}

func TestAuthExchangeInvalidResponseAndRedaction(t *testing.T) {
	for i, body := range []string{"SECRET_BROKEN_JSON", `{"access_token":""}`, `{"access_token":123}`} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			o, _, _ := fixture(t)
			os.Remove(o.Auth.TokenPath)
			o.Auth.Env = func(string) string { return "SECRET_API_KEY" }
			o.HTTPClient = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil })}
			r := Run(context.Background(), o)
			if check(t, r, "cloud_access").Code != "INVALID_RESPONSE" {
				t.Fatal(r)
			}
			assertNoSecrets(t, r)
		})
	}
}

func TestExistingNullProjectConfigIsInvalid(t *testing.T) {
	o, _, _ := fixture(t)
	writeFile(t, o.GatewayPath, "null\n")
	writeFile(t, o.SpyPath, "")
	r := Run(context.Background(), o)
	if check(t, r, "gateway_config").Code != "PROJECT_CONFIG_INVALID" || check(t, r, "spy_config").Code != "PROJECT_CONFIG_INVALID" {
		t.Fatal(r)
	}
}

func TestRequestedTimeoutOverridesInheritedClientTimeout(t *testing.T) {
	for _, source := range []string{"cloud", "auth"} {
		for _, credentials := range []string{"cached", "api-key"} {
			t.Run(source+"/"+credentials, func(t *testing.T) {
				o, _, _ := fixture(t)
				o.Timeout = 60 * time.Second
				if credentials == "api-key" {
					if err := os.Remove(o.Auth.TokenPath); err != nil {
						t.Fatal(err)
					}
					o.Auth.Env = func(string) string { return "SECRET_API_KEY" }
				}
				var deadline time.Time
				calls := 0
				var minimumDeadline time.Time
				client := &http.Client{Timeout: 30 * time.Second, Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					d, ok := req.Context().Deadline()
					if !ok || d.Before(minimumDeadline) {
						t.Fatalf("request deadline %v was shortened by the inherited client timeout; want at least %v", d, minimumDeadline)
					}
					if calls == 1 {
						deadline = d
					} else if d != deadline {
						t.Fatal("API-key exchange and Cloud probe did not share a deadline")
					}
					if req.Method == http.MethodPost {
						return response(200, `{"access_token":"SECRET_EXCHANGED"}`), nil
					}
					return response(200, `{"databuses":{}}`), nil
				})}
				if source == "cloud" {
					o.HTTPClient = client
				} else {
					o.HTTPClient = nil
					o.Auth.HTTPClient = client
				}
				minimumDeadline = time.Now().Add(o.Timeout)
				r := Run(context.Background(), o)
				if c := check(t, r, "cloud_access"); c.Status != "pass" {
					t.Fatalf("%+v", c)
				}
				wantCalls := 1
				if credentials == "api-key" {
					wantCalls = 2
				}
				if calls != wantCalls {
					t.Fatalf("got %d requests; want %d", calls, wantCalls)
				}
				if client.Timeout != 30*time.Second {
					t.Fatal("doctor changed the shared runtime client's timeout")
				}
			})
		}
	}
}
