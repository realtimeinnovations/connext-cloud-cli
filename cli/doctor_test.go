// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/rtipaths"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/update"
)

type doctorTransport func(*http.Request) (*http.Response, error)

func (f doctorTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func doctorRuntime(t *testing.T) *app.Runtime {
	t.Helper()
	home := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "LocalAppData"))
	oldPaths, oldHome, oldPlatform := rtipaths.UserHomeDir, connext.UserHomeDir, connext.Platform
	rtipaths.UserHomeDir = func() (string, error) { return home, nil }
	connext.UserHomeDir = rtipaths.UserHomeDir
	connext.Platform = func() (string, string) { return "linux", "amd64" }
	t.Cleanup(func() { rtipaths.UserHomeDir, connext.UserHomeDir, connext.Platform = oldPaths, oldHome, oldPlatform })
	t.Setenv("NDDSHOME", "")
	t.Setenv("RTI_LICENSE_FILE", "")
	t.Setenv("CONNEXT_CLOUD_API_KEY", "")
	return app.NewRuntime(home, io.Discard)
}

func TestDoctorFailedReportStdoutOnly(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			rt := doctorRuntime(t)
			var stdout, stderr bytes.Buffer
			err := Execute([]string{"doctor", "--format", format}, &stdout, &stderr, rt)
			if code := ReportError(err, &stderr); code != 1 {
				t.Fatalf("exit=%d error=%v", code, err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("extra error: %s", stderr.String())
			}
			if format == "json" {
				dec := json.NewDecoder(&stdout)
				var envelope struct {
					SchemaVersion string `json:"schema_version"`
					Data          struct {
						ExitCode int   `json:"exit_code"`
						Checks   []any `json:"checks"`
					} `json:"data"`
				}
				if err := dec.Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.SchemaVersion != "1" || envelope.Data.ExitCode != 1 || len(envelope.Data.Checks) == 0 {
					t.Fatal(envelope)
				}
				if dec.Decode(new(any)) != io.EOF {
					t.Fatal("extra stdout")
				}
			} else if !strings.Contains(stdout.String(), "No Cloud endpoint configured") {
				t.Fatal(stdout.String())
			}
		})
	}
}

func TestDoctorArgumentErrorsUseNormalJSONErrorContract(t *testing.T) {
	for _, args := range [][]string{{"doctor", "--format", "json", "--timeout", "0s"}, {"doctor", "extra", "--format", "json"}, {"doctor", "--format", "json", "--unknown"}, {"doctor", "--format", "yaml"}} {
		rt := doctorRuntime(t)
		var stdout, stderr bytes.Buffer
		err := Execute(args, &stdout, &stderr, rt)
		if code := ReportError(err, &stderr); code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Fatalf("%v: code=%d stdout=%s stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestDoctorSkipsUpdateWithLeadingGlobalFlag(t *testing.T) {
	rt := doctorRuntime(t)
	if err := rt.Config.WriteConfig(map[string]string{"api_host": "https://example.test/api/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Auth.SaveAccessToken("secret-test-token", 3600); err != nil {
		t.Fatal(err)
	}
	rt.CloudAPI.HTTPClient = &http.Client{Transport: doctorTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"databuses":{}}`))}, nil
	})}
	// The updater must not run even when argv[0] is a global flag.
	rt.Updater = update.New(rt.Config, io.Discard)
	rt.Updater.CurrentVersion = func() string { return "1.0.0" }
	rt.Updater.HTTPClient = &http.Client{Transport: doctorTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatal("doctor invoked update check")
		return nil, nil
	})}
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"--non-interactive=false", "doctor"}, &stdout, &stderr, rt)
	if err != nil || stderr.Len() != 0 {
		t.Fatalf("%v %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "HTTP 200") {
		t.Fatal(stdout.String())
	}
}

func TestDoctorDoesNotDeleteExpiredCredentials(t *testing.T) {
	rt := doctorRuntime(t)
	if err := rt.Config.WriteConfig(map[string]string{"api_host": "https://example.test/api/v1"}); err != nil {
		t.Fatal(err)
	}
	body := `{"access_token":"secret-expired","expires_at":"` + time.Now().Add(-time.Hour).Format(time.RFC3339) + `"}`
	os.MkdirAll(filepath.Dir(rt.Auth.TokenPath), 0700)
	os.WriteFile(rt.Auth.TokenPath, []byte(body), 0600)
	rt.CloudAPI.HTTPClient = &http.Client{Transport: doctorTransport(func(*http.Request) (*http.Response, error) { t.Fatal("probe without credentials"); return nil, nil })}
	var stdout, stderr bytes.Buffer
	err := Execute([]string{"doctor", "--format", "json"}, &stdout, &stderr, rt)
	if code := ReportError(err, &stderr); code != 3 {
		t.Fatalf("exit %d", code)
	}
	after, err := os.ReadFile(rt.Auth.TokenPath)
	if err != nil || string(after) != body {
		t.Fatal("credentials changed")
	}
	if strings.Contains(stdout.String(), "secret-expired") || stderr.Len() != 0 {
		t.Fatal("leaked credentials or extra report")
	}
}
