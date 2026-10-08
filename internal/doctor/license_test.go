// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package doctor

import (
	"strings"
	"testing"
	"time"
)

func TestLicenseMetadataCalendarBoundaries(t *testing.T) {
	// Date-only arithmetic remains stable across DST and local/UTC boundaries.
	now := time.Date(2026, 10, 3, 23, 59, 0, 0, time.FixedZone("PDT", -7*3600))
	cases := []struct {
		date   string
		status LicenseStatus
		days   int
	}{
		{"18-oct-2026", "current", 15}, {"17-oct-2026", "expiring", 14}, {"03-oct-2026", "expiring", 0}, {"02-oct-2026", "expired", -1}, {"12-OCT-26", "expiring", 9},
	}
	for _, tc := range cases {
		t.Run(tc.date, func(t *testing.T) {
			f := licenseFeatures([]byte("FEATURE RTIPRO RTI 4. "+tc.date+" SECRET_LICENSE"), now)
			if len(f) != 1 || f[0].Status != tc.status || f[0].DaysRemaining == nil || *f[0].DaysRemaining != tc.days {
				t.Fatalf("%+v", f)
			}
		})
	}
}
func TestUnknownPermanentAndMultipleLicenseRecords(t *testing.T) {
	data := `# Expires 1-jan-2001 (comment must not be parsed)
FEATURE RTIPRO RTI 4. permanent SECRET_LICENSE
INCREMENT RTISECURITY RTI 5. 12-oct-2026 SECRET_LICENSE
FEATURE RTIRWT RTI 6. 00-jan-00 SECRET_LICENSE
FEATURE RTICDS
FEATURE RTIPRO RTI 4. 01-oct-2026 SECRET_LICENSE`
	f := licenseFeatures([]byte(data), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	if len(f) != 5 || f[0].Status != "permanent" || f[0].DaysRemaining != nil || f[2].Status != "unknown" || f[3].Status != "unknown" || f[4].Status != "expired" {
		t.Fatalf("%+v", f)
	}
	for _, record := range f {
		if strings.Contains(record.description(), "SECRET") {
			t.Fatal("leak")
		}
	}
}
