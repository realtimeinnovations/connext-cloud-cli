// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package doctor

import (
	"fmt"
	"strings"
	"time"
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

func licenseFeatures(contents []byte, now time.Time) []LicenseFeature {
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
	if !strings.HasPrefix(s, "RTI") || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') && r != '_' {
			return false
		}
	}
	return true
}

func (f LicenseFeature) description() string {
	if f.Status == LicensePermanent {
		return "No expiration (declared in file)"
	}
	if f.DaysRemaining == nil {
		return "Expiration unknown"
	}
	switch days := *f.DaysRemaining; {
	case days < 0:
		return fmt.Sprintf("Expired %d days ago · %s", -days, *f.ExpiresOn)
	case days == 0:
		return "Expires today · " + *f.ExpiresOn
	default:
		return fmt.Sprintf("Expires in %d days · %s", days, *f.ExpiresOn)
	}
}
