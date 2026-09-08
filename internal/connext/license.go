// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
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

func HasLicenseAvailable(install Install) bool {
	if selected, err := os.Lstat(LicenseFilePath(install)); err == nil {
		if !selected.Mode().IsRegular() {
			return false
		}
		_, err = readCopyableLicense(LicenseFilePath(install))
		return err == nil
	} else if !os.IsNotExist(err) {
		return false
	}
	if path := os.Getenv("RTI_LICENSE_FILE"); path != "" {
		_, err := readCopyableLicense(path)
		return err == nil
	}
	return false
}

func LicenseFilePath(install Install) string {
	return filepath.Join(install.Path, LicenseFileName)
}

func WriteLicenseFile(install Install, content []byte) error {
	if IsManagedInstallation(install) {
		canonical, err := managedLicensePath()
		if err != nil {
			return err
		}
		if err := writeVerifiedLicense(canonical, content); err != nil {
			return err
		}
		return writeVerifiedLicense(LicenseFilePath(install), content)
	}
	return os.WriteFile(LicenseFilePath(install), content, 0o600)
}
