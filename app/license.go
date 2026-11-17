// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package app

import (
	"fmt"
	"os"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
)

// UseConnextLicense saves a copy; the supplied file is never changed.
func (runtime *Runtime) UseConnextLicense(source string, yes bool) (string, error) {
	if err := checkLicenseOverride(); err != nil {
		return "", err
	}
	_, contents, err := connext.ReadLicenseSource(source)
	if err != nil {
		return "", err
	}
	if err := connext.ValidateReplacementLicense(contents); err != nil {
		return "", err
	}
	if err := runtime.confirmSavedLicenseReplacement(yes); err != nil {
		return "", err
	}
	return connext.SaveManagedLicense(contents)
}

// RefreshConnextLicense makes one authorized download; failure preserves the store.
func (runtime *Runtime) RefreshConnextLicense(yes bool) (string, error) {
	if err := checkLicenseOverride(); err != nil {
		return "", err
	}
	if err := runtime.confirmSavedLicenseReplacement(yes); err != nil {
		return "", err
	}
	if runtime.License == nil {
		return "", fmt.Errorf("Connext license download is not configured")
	}
	contents, err := runtime.License.DownloadLicense(nil)
	if err != nil {
		return "", fmt.Errorf("Connext license download failed; saved license unchanged: %w", err)
	}
	if err := connext.ValidateReplacementLicense(contents); err != nil {
		return "", err
	}
	return connext.SaveManagedLicense(contents)
}

func checkLicenseOverride() error {
	if os.Getenv("RTI_LICENSE_FILE") != "" {
		return clierror.InputRequired("RTI_LICENSE_FILE is active. It selects a file directly and overrides the saved license. Unset RTI_LICENSE_FILE before changing the saved license, or update your selected file yourself. No license files were changed.", "review_license_override")
	}
	return nil
}

func (runtime *Runtime) confirmSavedLicenseReplacement(yes bool) error {
	path, err := connext.SavedLicensePath()
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("saved license must be a regular file: %s", path)
	}
	if yes {
		return nil
	}
	if runtime.NonInteractive || runtime.Gateway == nil || runtime.Gateway.SelectFunc == nil {
		return clierror.InputRequired("Replacing the saved license at "+path+" requires confirmation. Rerun interactively or add --yes to authorize replacement.", "confirm_license_replacement")
	}
	accepted, err := connext.ConfirmationFromSelector(runtime.Gateway.SelectFunc).Confirm(connext.Confirmation{
		Message:     "Replace the saved managed license at " + path + "? Original user files and installation licenses will be preserved.",
		AcceptLabel: "Replace saved license", DeclineLabel: "Cancel",
	})
	if err != nil {
		return err
	}
	if !accepted {
		return clierror.InputRequired("License replacement cancelled; no license files were changed.", "confirm_license_replacement")
	}
	return nil
}
