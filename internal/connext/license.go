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
	if envLicenseFile := os.Getenv("RTI_LICENSE_FILE"); envLicenseFile != "" {
		if _, err := os.Stat(envLicenseFile); err == nil {
			return true
		}
	}
	_, err := os.Stat(LicenseFilePath(install))
	return err == nil
}

func LicenseFilePath(install Install) string {
	return filepath.Join(install.Path, LicenseFileName)
}

func WriteLicenseFile(install Install, content []byte) error {
	return os.WriteFile(LicenseFilePath(install), content, 0o644)
}
