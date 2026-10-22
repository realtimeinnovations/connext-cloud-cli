// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const LicenseFileName = "rti_license.dat"

func IsLicenseManaged(install Install) bool {
	file, err := os.Open(filepath.Join(install.Path, "rti_versions.xml"))
	if err != nil {
		return false
	}
	defer file.Close()

	decoder := xml.NewDecoder(file)
	inHost := false
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "host" {
				inHost = true
				continue
			}
			if inHost && typed.Name.Local == "installation_type" {
				var installationType string
				if err := decoder.DecodeElement(&installationType, &typed); err != nil {
					return false
				}
				return strings.Contains(strings.ToUpper(installationType), "LM")
			}
		case xml.EndElement:
			if typed.Name.Local == "host" {
				inHost = false
			}
		}
	}
}

// HasLicenseAvailable checks the external installation's local license. Explicit
// environment licenses are prechecked separately before reaching this fallback.
func HasLicenseAvailable(install Install) bool {
	_, err := readCopyableLicense(LicenseFilePath(install))
	return err == nil
}

func LicenseFilePath(install Install) string {
	return filepath.Join(install.Path, LicenseFileName)
}

// WriteLicenseFile validates only new or changed license contents. Reusing a
// license or repairing an installation copy does not require another override.
func WriteLicenseFile(install Install, content []byte, confirm Confirmer) error {
	_, err := writeLicenseFile(install, content, confirm, "")
	return err
}

// A nonempty source requests import confirmation after checking changed contents
// and before writing either copy. Downloads need only the feature check.
func writeLicenseFile(install Install, content []byte, confirm Confirmer, source string) (bool, error) {
	managed := IsManagedInstallation(install)
	target := LicenseFilePath(install)
	if managed {
		var err error
		target, err = managedLicensePath()
		if err != nil {
			return false, err
		}
		if err := safeManagedPath(filepath.Dir(filepath.Dir(filepath.Dir(target))), filepath.Dir(target)); err != nil {
			return false, err
		}
	}
	current, err := readCopyableLicense(target)
	changed := err != nil || !bytes.Equal(current, content)
	var problems []string
	checkStatus := "License contents match the saved license."
	if changed {
		problems = LicenseFeatureProblems(ParseLicenseFeatures(content, time.Now()))
		checkedPath := target
		if source != "" {
			checkedPath = source
		}
		if err := confirmLicenseFeatureProblems(checkedPath, problems, confirm); err != nil {
			return false, err
		}
		checkStatus = "License feature check passed: RTIPRO, RTISECURITY, RTIRWT."
		if len(problems) > 0 {
			checkStatus = "License feature warnings were accepted."
		}
	}
	if source != "" {
		if confirm == nil {
			return false, fmt.Errorf("copying the Connext license file from %s to %s requires explicit confirmation. Run rticloud gateway or rticloud spy in an interactive terminal to copy and use this license file, or download a license from evaluation.rti.com", source, target)
		}
		accepted, err := confirm.Confirm(Confirmation{
			Message:       fmt.Sprintf("Found an existing Connext license:\n  %s\n\n%s\n\nThis license file will be copied to:\n  %s\n\nThis license will be used when rticloud needs to run Connext Professional components.", source, checkStatus, target),
			AcceptLabel:   ImportManagedLicenseLabel,
			DeclineLabel:  DeclineManagedLicenseLabel,
			DefaultAccept: len(problems) == 0,
		})
		if err != nil || !accepted {
			return false, err
		}
	}
	if changed {
		if managed {
			if err := writeVerifiedLicense(target, content); err != nil {
				return false, err
			}
		} else if err := os.WriteFile(target, content, 0o600); err != nil {
			return false, err
		}
	}
	if managed {
		local := LicenseFilePath(install)
		current, err := readCopyableLicense(local)
		if err != nil || !bytes.Equal(current, content) {
			if err := writeVerifiedLicense(local, content); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}
