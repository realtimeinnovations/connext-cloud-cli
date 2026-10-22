// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package doctor

import (
	"fmt"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
)

func licenseFeatureDescription(f connext.LicenseFeature) string {
	if f.Status == connext.LicensePermanent {
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
