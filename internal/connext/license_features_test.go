// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
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

func TestLicenseUpdatesValidateOnlyChangedContents(t *testing.T) {
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
			if err := WriteLicenseFile(install, []byte(incomplete), nil); err != nil {
				t.Fatalf("unchanged license required approval: %v", err)
			}
			after, err := os.Stat(target)
			if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("unchanged license was rewritten: %v", err)
			}
			if managed {
				if err := os.Remove(LicenseFilePath(install)); err != nil {
					t.Fatal(err)
				}
				if available, err := ProvisionManagedLicense(install, nil); err != nil || !available {
					t.Fatalf("repair of existing license required approval: available=%v err=%v", available, err)
				}
				if data, err := os.ReadFile(LicenseFilePath(install)); err != nil || string(data) != incomplete {
					t.Fatalf("existing license was not repaired: %v", err)
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
			if err := WriteLicenseFile(install, []byte(validTestConnextLicense), nil); err != nil {
				t.Fatalf("complete updated license required approval: %v", err)
			}
		})
	}
}
