// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
)

type LicenseStatus string

const (
	LicenseUnknown   LicenseStatus = "unknown"
	LicensePermanent LicenseStatus = "permanent"
	LicenseCurrent   LicenseStatus = "current"
	LicenseExpired   LicenseStatus = "expired"
	LicenseExpiring  LicenseStatus = "expiring"
)

// LicenseFeature is unverified, human-readable FEATURE/INCREMENT metadata.
// Never include signatures, license keys, or the original record in a report.
type LicenseFeature struct {
	Feature       string        `json:"feature"`
	ExpiresOn     *string       `json:"expires_on"`
	DaysRemaining *int          `json:"days_remaining"`
	Status        LicenseStatus `json:"status"`
}

// ParseLicenseFeatures reads only reportable metadata; it does not validate signatures.
func ParseLicenseFeatures(contents []byte, now time.Time) []LicenseFeature {
	var features []LicenseFeature
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (fields[0] != "FEATURE" && fields[0] != "INCREMENT") {
			continue
		}
		f := LicenseFeature{Feature: "unknown", Status: LicenseUnknown}
		if len(fields) > 1 {
			// Only RTI feature identifiers are reportable, never arbitrary file text.
			if validFeature(fields[1]) {
				f.Feature = fields[1]
			}
		}
		if len(fields) >= 5 && f.Feature != "unknown" && fields[2] == "RTI" {
			value := strings.ToLower(fields[4])
			if value == "permanent" {
				f.Status = LicensePermanent
			} else {
				for _, layout := range []string{"2-Jan-2006", "2-Jan-06"} {
					expiration, err := time.Parse(layout, value)
					if err != nil {
						continue
					}
					date := expiration.Format("2006-01-02")
					days := int(expiration.Sub(today) / (24 * time.Hour))
					f.ExpiresOn, f.DaysRemaining = &date, &days
					f.Status = LicenseCurrent
					if days < 0 {
						f.Status = LicenseExpired
					} else if days <= 14 {
						f.Status = LicenseExpiring
					}
					break
				}
			}
		}
		features = append(features, f)
	}
	return features
}

func validFeature(s string) bool {
	if (!strings.HasPrefix(s, "RTI") && s != "CONNEXTPRO") || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' {
			return false
		}
	}
	return true
}

// RequiredLicenseFeatures is the common Gateway/Spy baseline, independent of
// command and security configuration. RTICDS activates the discovery service
// itself and is not required by these clients.
func RequiredLicenseFeatures() []string {
	return []string{"RTIPRO", "RTISECURITY", "RTIRWT"}
}

// LicenseFeatureProblems checks declared feature availability and expiry only.
// One current or permanent alternative grant is enough; signatures and runtime
// acceptance can only be checked by Connext itself.
func LicenseFeatureProblems(features []LicenseFeature) []string {
	var problems []string
	for _, required := range RequiredLicenseFeatures() {
		found, current, unknown := false, false, false
		for _, feature := range features {
			// Connext also accepts its combined product grants.
			matches := feature.Feature == required || feature.Feature == "CONNEXTPRO" || (required == "RTIRWT" && feature.Feature == "RTIANYWHERE")
			if !matches {
				continue
			}
			found = true
			switch feature.Status {
			case LicensePermanent, LicenseCurrent, LicenseExpiring:
				current = true
			case LicenseUnknown:
				unknown = true
			}
		}
		switch {
		case current:
		case !found:
			problems = append(problems, "Missing required feature: "+required)
		case unknown:
			problems = append(problems, "Cannot verify expiration of required feature: "+required)
		default:
			problems = append(problems, "Required feature has only expired records: "+required)
		}
	}
	return problems
}

const (
	DeclineLicensePrecheckLabel = "No, stop"
	ProceedLicensePrecheckLabel = "Yes, proceed anyway"
)

// PrecheckEnvironmentLicense checks an explicit runtime override on every launch.
// It never imports the file or changes the stored license.
func PrecheckEnvironmentLicense(confirm Confirmer, out io.Writer) error {
	path := environmentLicensePath()
	const unsetHint = "Unset RTI_LICENSE_FILE and rerun this command to use a different license."
	if out != nil {
		_, _ = fmt.Fprintf(out, "Connext license found via RTI_LICENSE_FILE:\n  %s\n\n%s\n", path, unsetHint)
	}
	contents, err := readEnvironmentLicense(path)
	if err != nil {
		return fmt.Errorf("reading license from RTI_LICENSE_FILE (%s): %w\n%s", path, err, unsetHint)
	}
	problems := LicenseFeatureProblems(ParseLicenseFeatures(contents, time.Now()))
	if err := confirmLicenseFeatureProblems("RTI_LICENSE_FILE="+path, problems, confirm); err != nil {
		return fmt.Errorf("%w\n%s", err, unsetHint)
	}
	if len(problems) == 0 && out != nil {
		_, _ = fmt.Fprintln(out, "License feature check passed: RTIPRO, RTISECURITY, RTIRWT.")
	}
	return nil
}

// Connext accepts outer quotes. Resolve relative paths before a launcher changes
// directories so the precheck and child process use the same file.
func environmentLicensePath() string {
	path := strings.Trim(os.Getenv("RTI_LICENSE_FILE"), "\"'")
	if path != "" {
		if absolute, err := filepath.Abs(path); err == nil {
			return absolute
		}
	}
	return path
}

// Stored licenses are checked only when contents change. An explicit environment
// override is checked on each launch because it bypasses the saved license.
func confirmLicenseFeatureProblems(source string, problems []string, confirm Confirmer) error {
	if len(problems) == 0 {
		return nil
	}
	message := fmt.Sprintf("Connext license precheck found problems:\n  %s\n\n- %s\n\nConnext may fail to start or connect.\nProceed anyway?", source, strings.Join(problems, "\n- "))
	if confirm == nil {
		return clierror.InputRequired(message+"\nRun interactively to explicitly approve proceeding, or obtain a license from evaluation.rti.com.", "review_connext_license")
	}
	accepted, err := confirm.Confirm(Confirmation{
		Kind:         ConfirmLicenseFeatures,
		Message:      message,
		AcceptLabel:  ProceedLicensePrecheckLabel,
		DeclineLabel: DeclineLicensePrecheckLabel,
	})
	if err != nil {
		return err
	}
	if !accepted {
		return common.UserError{Message: "Connext license precheck declined. Obtain a license from evaluation.rti.com, or provide a license file with RTIPRO, RTISECURITY, and RTIRWT before retrying."}
	}
	return nil
}
