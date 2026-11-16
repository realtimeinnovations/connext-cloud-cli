// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
)

func TestLicenseFeatureProblems(t *testing.T) {
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, contents, want string
	}{
		{"complete-without-cds", validTestConnextLicense, ""},
		{"missing-security", strings.ReplaceAll(validTestConnextLicense, "RTISECURITY", "RTIOTHER"), "Missing required feature: RTISECURITY"},
		{"missing-pro", strings.ReplaceAll(validTestConnextLicense, "RTIPRO", "RTIOTHER"), "Missing required feature: RTIPRO"},
		{"missing-rwt", strings.ReplaceAll(validTestConnextLicense, "RTIRWT", "RTIOTHER"), "Missing required feature: RTIRWT"},
		{"empty", "", "Missing required feature: RTIPRO\nMissing required feature: RTISECURITY\nMissing required feature: RTIRWT"},
		{"comment-only", "# FEATURE RTIPRO RTI 4. permanent SECRET_LICENSE", "Missing required feature: RTIPRO\nMissing required feature: RTISECURITY\nMissing required feature: RTIRWT"},
		{"expired-security", strings.ReplaceAll(validTestConnextLicense, "RTISECURITY RTI 5. permanent", "RTISECURITY RTI 5. 21-oct-2026"), "Required feature has only expired records: RTISECURITY"},
		{"unknown-date", strings.ReplaceAll(validTestConnextLicense, "RTISECURITY RTI 5. permanent", "RTISECURITY RTI 5. invalid"), "Cannot verify expiration of required feature: RTISECURITY"},
		{"wrong-vendor", strings.ReplaceAll(validTestConnextLicense, "RTISECURITY RTI", "RTISECURITY OTHER"), "Cannot verify expiration of required feature: RTISECURITY"},
		{"current-alternative", validTestConnextLicense + "FEATURE RTISECURITY RTI 5. 01-jan-2000 SECRET_LICENSE", ""},
		{"combined-pro", "FEATURE CONNEXTPRO RTI 7. permanent SECRET_LICENSE", ""},
		{"anywhere-rwt", strings.ReplaceAll(validTestConnextLicense, "RTIRWT", "RTIANYWHERE"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			features := ParseLicenseFeatures([]byte(tc.contents), now)
			got := strings.Join(LicenseFeatureProblems(features), "\n")
			if got != tc.want {
				t.Fatalf("problems = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLicenseReuseRechecksMetadataWithoutRewriting(t *testing.T) {
	for _, managed := range []bool{true, false} {
		t.Run(map[bool]string{true: "managed", false: "external"}[managed], func(t *testing.T) {
			root := managedTestEnvironment(t)
			install := Install{Path: t.TempDir()}
			if managed {
				install = Install{Path: writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion), Version: installerVersion}
			}
			target := LicenseFilePath(install)
			if managed {
				target, _ = managedLicensePath()
			}
			const incomplete = "FEATURE RTIPRO RTI 4. 01-jan-2000 SECRET_LICENSE\n"
			approvals := 0
			confirm := ConfirmFunc(func(request Confirmation) (bool, error) {
				if request.AcceptLabel == "Replace installation license" {
					return true, nil
				}
				approvals++
				if request.AcceptLabel != ProceedLicensePrecheckLabel {
					t.Fatalf("unexpected confirmation: %+v", request)
				}
				return true, nil
			})
			if err := WriteLicenseFile(install, []byte(incomplete), confirm); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(target)
			if err != nil {
				t.Fatal(err)
			}
			if err := WriteLicenseFile(install, []byte(incomplete), nil); err == nil || clierror.From(err).Code != clierror.CodeInputRequired {
				t.Fatalf("unchanged expired license escaped precheck: %v", err)
			}
			after, err := os.Stat(target)
			if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("unchanged license was rewritten: %v", err)
			}
			if managed {
				if available, err := ProvisionManagedLicense(install, nil); err == nil || available {
					t.Fatalf("expired saved license escaped launch precheck: available=%v err=%v", available, err)
				}
				if data, err := os.ReadFile(LicenseFilePath(install)); err != nil || string(data) != incomplete {
					t.Fatalf("expired installation copy changed: %v", err)
				}
			}
			changed := []byte(incomplete + "# updated\n")
			if err := WriteLicenseFile(install, changed, nil); err == nil || clierror.From(err).Code != clierror.CodeInputRequired {
				t.Fatalf("changed license must require interactive approval: %v", err)
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != incomplete {
				t.Fatalf("unapproved update changed the license: %v", err)
			}
			if err := WriteLicenseFile(install, changed, confirm); err != nil {
				t.Fatal(err)
			}
			if approvals != 2 {
				t.Fatalf("approvals = %d, want one per content change", approvals)
			}
			if err := WriteLicenseFile(install, []byte(validTestConnextLicense), confirm); err != nil {
				t.Fatalf("complete updated license required approval: %v", err)
			}
		})
	}
}

func TestManagedLicenseReplacementConsentPreservesBothFiles(t *testing.T) {
	for _, operation := range []string{"reuse", "download"} {
		for _, answer := range []string{"noninteractive", "", "\n", "2\n"} {
			t.Run(operation+"/"+answer, func(t *testing.T) {
				root := managedTestEnvironment(t)
				install := Install{Path: writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion), Version: installerVersion}
				saved, _ := managedLicensePath()
				local := LicenseFilePath(install)
				originalSaved := validTestConnextLicense + "# saved\n"
				originalLocal := validTestConnextLicense + "# user installation\n"
				if err := os.MkdirAll(filepath.Dir(saved), 0700); err != nil {
					t.Fatal(err)
				}
				for path, contents := range map[string]string{saved: originalSaved, local: originalLocal} {
					if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
						t.Fatal(err)
					}
				}
				var out bytes.Buffer
				var confirm Confirmer
				if answer != "noninteractive" {
					confirm = scriptedConfirmation(answer, &out)
				}
				candidate := originalSaved
				var err error
				if operation == "reuse" {
					_, err = ProvisionManagedLicense(install, confirm)
				} else {
					candidate = validTestConnextLicense + "# new download\n"
					err = WriteLicenseFile(install, []byte(candidate), confirm)
				}
				accepted := answer == "2\n"
				if (err == nil) != accepted {
					t.Fatalf("accepted=%v err=%v", accepted, err)
				}
				wantSaved, wantLocal := originalSaved, originalLocal
				if accepted {
					wantSaved, wantLocal = candidate, candidate
				}
				for path, want := range map[string]string{saved: wantSaved, local: wantLocal} {
					if got, err := os.ReadFile(path); err != nil || string(got) != want {
						t.Fatalf("unexpected file contents at %s: %v", path, err)
					}
				}
				if accepted {
					before, _ := os.Stat(local)
					if available, err := ProvisionManagedLicense(install, nil); err != nil || !available {
						t.Fatalf("matching copy prompted: %v", err)
					}
					after, _ := os.Stat(local)
					if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
						t.Fatal("matching installation copy rewritten")
					}
				}
			})
		}
	}
}

func TestRenewalAdviceHonorsAlternativeGrants(t *testing.T) {
	now := time.Now()
	soon := now.AddDate(0, 0, 7).Format("02-Jan-2006")
	later := now.AddDate(0, 0, 40).Format("02-Jan-2006")
	expiring := "FEATURE CONNEXTPRO RTI 7. " + soon + " SIGN=TEST_ONLY\n"
	for _, tc := range []struct {
		name, contents string
		renew          bool
	}{
		{"only-expiring", expiring, true},
		{"permanent-alternative", expiring + validTestConnextLicense, false},
		{"later-alternative", expiring + "FEATURE CONNEXTPRO RTI 7. " + later + " SIGN=TEST_ONLY\n", false},
		{"expired-alternative", expiring + "FEATURE CONNEXTPRO RTI 7. 01-jan-2000 SIGN=TEST_ONLY\n", true},
		{"optional-feature", validTestConnextLicense + "FEATURE RTICDS RTI 7. " + soon + " SIGN=TEST_ONLY\n", false},
		{"anywhere-alternative", strings.ReplaceAll(validTestConnextLicense, "RTIRWT RTI 6. permanent", "RTIRWT RTI 6. "+soon) + "FEATURE RTIANYWHERE RTI 7. permanent SIGN=TEST_ONLY\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			contents := []byte(tc.contents)
			if got := LicenseNeedsRenewal(ParseLicenseFeatures(contents, now)); got != tc.renew {
				t.Fatalf("renew=%v", got)
			}
			var out bytes.Buffer
			WarnLicenseExpiration(contents, &out)
			if strings.Contains(out.String(), "rticloud license refresh") != tc.renew {
				t.Fatalf("unexpected advice: %s", out.String())
			}
			if !strings.Contains(out.String(), now.AddDate(0, 0, 7).Format("2006-01-02")) {
				t.Fatalf("missing record date: %s", out.String())
			}
		})
	}
}
