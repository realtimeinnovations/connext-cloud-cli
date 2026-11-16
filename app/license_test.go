// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package app

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/commands"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	internalconnext "github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
)

func TestLicenseRecoveryPreservesFilesOnFailure(t *testing.T) {
	const expired = "FEATURE CONNEXTPRO RTI 7. 01-jan-2000 SIGN=TEST_ONLY\n"
	for _, tc := range []struct {
		name, body    string
		status        int
		yes, override bool
		calls         int
		success       bool
	}{
		{name: "current", body: runtimeValidConnextLicense, status: 200, yes: true, calls: 1, success: true},
		{name: "expired", body: expired, status: 200, yes: true, calls: 1},
		{name: "empty", status: 200, yes: true, calls: 1},
		{name: "unexpected-json", body: `{"error":"unavailable"}`, status: 200, yes: true, calls: 1},
		{name: "denied", body: `{"message":"denied"}`, status: 403, yes: true, calls: 1},
		{name: "confirmation-required", body: runtimeValidConnextLicense, status: 200},
		{name: "override", body: runtimeValidConnextLicense, status: 200, yes: true, override: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, install, shared := managedLicenseRuntime(t)
			rt.NonInteractive = true
			if err := os.MkdirAll(filepath.Dir(shared), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shared, []byte(expired), 0600); err != nil {
				t.Fatal(err)
			}
			local := internalconnext.LicenseFilePath(install)
			if err := os.WriteFile(local, []byte(runtimeValidConnextLicense), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.override {
				t.Setenv("RTI_LICENSE_FILE", local)
			}
			api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(tc.status, tc.body)}}
			rt.License = commands.New(api, io.Discard)
			path, err := rt.RefreshConnextLicense(tc.yes)
			if (err == nil) != tc.success || api.calls != tc.calls {
				t.Fatalf("success=%v err=%v calls=%d", tc.success, err, api.calls)
			}
			want := expired
			if tc.success {
				want = runtimeValidConnextLicense
				if path != shared {
					t.Fatalf("path=%s", path)
				}
			}
			for file, expected := range map[string]string{shared: want, local: runtimeValidConnextLicense} {
				data, err := os.ReadFile(file)
				if err != nil || string(data) != expected {
					t.Fatalf("file changed unexpectedly %s: %v", file, err)
				}
			}
		})
	}
}

func TestUseLicensePreservesOriginalAndRequiresReplacementConsent(t *testing.T) {
	for _, answers := range []string{"\n", "2\n", ""} {
		t.Run("answer="+answers, func(t *testing.T) {
			rt, install, shared := managedLicenseRuntime(t)
			source := filepath.Join(t.TempDir(), "chosen license.dat")
			if err := os.WriteFile(source, []byte(runtimeValidConnextLicense), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := rt.UseConnextLicense(source, false); err != nil {
				t.Fatal(err)
			}
			old := runtimeValidConnextLicense + "# old saved copy\n"
			if err := os.WriteFile(shared, []byte(old), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			rt.Gateway = gateway.NewGatewayApp(t.TempDir(), &out)
			rt.Gateway.In = strings.NewReader(answers)
			_, err := rt.UseConnextLicense(source, false)
			want := old
			if answers == "2\n" {
				want = runtimeValidConnextLicense
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("replacement accepted by default or EOF")
			}
			for file, expected := range map[string]string{source: runtimeValidConnextLicense, shared: want} {
				data, err := os.ReadFile(file)
				if err != nil || string(data) != expected {
					t.Fatalf("unexpected bytes at %s: %v", file, err)
				}
			}
			if _, err := os.Stat(internalconnext.LicenseFilePath(install)); !os.IsNotExist(err) {
				t.Fatal("created installation copy")
			}
		})
	}
}

func TestExpiredSavedLicenseRecoveryConfirmsInstallationReplacement(t *testing.T) {
	for _, command := range []string{"gateway", "spy"} {
		t.Run(command, func(t *testing.T) {
			rt, install, shared := managedLicenseRuntime(t)
			expired := []byte("FEATURE CONNEXTPRO RTI 7. 01-jan-2000 SIGN=TEST_ONLY\n")
			if err := os.MkdirAll(filepath.Dir(shared), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(shared, expired, 0600); err != nil {
				t.Fatal(err)
			}
			local := internalconnext.LicenseFilePath(install)
			own := runtimeValidConnextLicense + "# user-owned\n"
			if err := os.WriteFile(local, []byte(own), 0600); err != nil {
				t.Fatal(err)
			}
			if err := rt.ensureConnextLicense(install, nil, nil); err == nil {
				t.Fatal("expired saved license bypassed precheck")
			}
			if data, _ := os.ReadFile(local); string(data) != own {
				t.Fatal("overwrote user file")
			}
			var out bytes.Buffer
			rt.Out = &out
			api := &runtimeFakeAPI{responses: map[string]*http.Response{"POST /licenses": runtimeTextResponse(200, runtimeValidConnextLicense)}}
			rt.License = commands.New(api, &out)
			selectFn, inputFn := licensePrompts(t, command, "3\n1\n2\n", &out)
			if err := rt.ensureConnextLicense(install, selectFn, inputFn); err != nil {
				t.Fatal(err)
			}
			if api.calls != 1 {
				t.Fatalf("downloads=%d", api.calls)
			}
			for file, want := range map[string]string{shared: runtimeValidConnextLicense, local: runtimeValidConnextLicense} {
				data, err := os.ReadFile(file)
				if err != nil || string(data) != want {
					t.Fatalf("unexpected bytes at %s: %v", file, err)
				}
			}
			if err := rt.ensureConnextLicense(install, nil, nil); err != nil {
				t.Fatal(err)
			}
			if api.calls != 1 {
				t.Fatal("current license triggered another download")
			}
		})
	}
}

func TestUseLicenseRejectsInvalidCandidatesAndSymlinkDestination(t *testing.T) {
	for _, kind := range []string{"expired", "empty", "destination-symlink", "override"} {
		t.Run(kind, func(t *testing.T) {
			rt, _, shared := managedLicenseRuntime(t)
			source := filepath.Join(t.TempDir(), "candidate.dat")
			contents := runtimeValidConnextLicense
			if kind == "expired" {
				contents = "FEATURE CONNEXTPRO RTI 7. 01-jan-2000 SIGN=TEST_ONLY\n"
			}
			if kind == "empty" {
				contents = ""
			}
			if err := os.WriteFile(source, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(shared), 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("original saved file")
			if kind == "destination-symlink" {
				if err := os.Symlink(source, shared); err != nil {
					t.Skip(err)
				}
			} else if err := os.WriteFile(shared, original, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "override" {
				t.Setenv("RTI_LICENSE_FILE", source)
			}
			if _, err := rt.UseConnextLicense(source, true); err == nil {
				t.Fatal("unsafe replacement accepted")
			}
			data, err := os.ReadFile(source)
			if err != nil || string(data) != contents {
				t.Fatalf("source changed: %v", err)
			}
			if kind == "destination-symlink" {
				if target, err := os.Readlink(shared); err != nil || target != source {
					t.Fatal("symlink changed")
				}
			} else if data, err := os.ReadFile(shared); err != nil || !bytes.Equal(data, original) {
				t.Fatalf("store changed: %v", err)
			}
		})
	}
}

func TestExternalLicenseReportsStatusWithoutRecovery(t *testing.T) {
	const expired = "FEATURE CONNEXTPRO RTI 7. 01-jan-2000 SIGN=TEST_ONLY\n"
	for _, tc := range []struct {
		name, contents, errorText string
		warn                      bool
	}{
		{name: "expired", contents: expired, errorText: "Connext license expired."},
		{name: "current", contents: runtimeValidConnextLicense},
		{name: "current-alternative", contents: expired + runtimeValidConnextLicense},
		{name: "expiring", contents: "FEATURE CONNEXTPRO RTI 7. " + time.Now().AddDate(0, 0, 7).Format("02-Jan-2006") + " SIGN=TEST_ONLY\n", warn: true},
		{name: "missing-features", contents: "# empty license\n", errorText: "Missing required feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _, shared := managedLicenseRuntime(t)
			install := internalconnext.Install{Path: t.TempDir(), Version: "7.7.0"}
			if err := os.WriteFile(filepath.Join(install.Path, "rti_versions.xml"), []byte("<rti><host><installation_type>LM</installation_type></host></rti>"), 0600); err != nil {
				t.Fatal(err)
			}
			local := internalconnext.LicenseFilePath(install)
			if err := os.WriteFile(local, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			rt.Out = &out
			selectFn := func(string, []string, string) (string, error) {
				t.Fatal("external license opened a recovery or confirmation prompt")
				return "", nil
			}
			for _, interactive := range []bool{false, true} {
				out.Reset()
				var err error
				if interactive {
					err = rt.ensureConnextLicense(install, selectFn, nil)
				} else {
					err = rt.ensureConnextLicense(install, nil, nil)
				}
				if tc.errorText == "" && err != nil || tc.errorText != "" && (err == nil || !strings.Contains(err.Error(), tc.errorText)) {
					t.Fatalf("unexpected error: %v", err)
				}
				if tc.name == "expired" && err.Error() != tc.errorText {
					t.Fatalf("expiry error includes extra advice: %v", err)
				}
				if strings.Contains(out.String(), "rticloud") || (out.Len() != 0) != tc.warn {
					t.Fatalf("unexpected output: %q", out.String())
				}
			}
			if got, err := os.ReadFile(local); err != nil || string(got) != tc.contents {
				t.Fatalf("installation license changed: %v", err)
			}
			if _, err := os.Stat(shared); !os.IsNotExist(err) {
				t.Fatalf("shared license was created: %v", err)
			}
		})
	}
}
