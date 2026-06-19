package connext

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
)

const (
	InstallConnextCloudExtrasLabel = "Install Connext Cloud Extras package"
	CancelPackageInstallLabel      = "Cancel"
	cloudExtrasBundleVersion       = "7.7.0"
)

type ComponentInstallation struct {
	Component     string
	Architecture  string
	Version       string
	Licensed      string
	FriendlyName  string
	InstallerName string
}

type capabilityPatch struct {
	Name        string
	CommandName string
	Check       func(Install) bool
}

type rtiVersionInstallation struct {
	Architecture  string `xml:"architecture"`
	Version       string `xml:"version"`
	Licensed      string `xml:"licensed"`
	FriendlyName  string `xml:"friendly_name"`
	InstallerName string `xml:"installer_name"`
}

var (
	PackageInstaller = runPackageInstaller
	versionPartsRE   = regexp.MustCompile(`\d+`)
)

func HasExecutable(install Install, executableName string) bool {
	_, err := os.Stat(Executable(install.Path, executableName))
	return err == nil
}

func HasCollectorServiceLite(install Install) bool {
	return HasExecutable(install, "rticollectorservicelite")
}

func HasEnhancedDDSSpy(install Install) bool {
	if !HasExecutable(install, "rtiddsspy") {
		return false
	}
	version := install.Version
	if version == "" {
		version = VersionFromPath(install.Path)
	}
	if CompareVersion(version, "7.7.1") >= 0 {
		return true
	}
	return HasCloudExtras(install) || isConnext770PatchRelease(version) || hasConnext770PatchReleaseMetadata(install.Path)
}

func HasCloudExtras(install Install) bool {
	components, err := InstalledComponents(install.Path)
	if err != nil {
		return false
	}
	for _, component := range components {
		installerName := strings.ToLower(component.InstallerName)
		version := strings.ToLower(component.Version)
		if strings.Contains(installerName, "cloud-extras.rtipkg") || strings.Contains(version, "_rti_er_723") {
			return true
		}
	}
	return false
}

func InstalledComponents(installPath string) ([]ComponentInstallation, error) {
	file, err := os.Open(filepath.Join(installPath, "rti_versions.xml"))
	if err != nil {
		return nil, err
	}
	defer file.Close()

	decoder := xml.NewDecoder(file)
	components := []ComponentInstallation{}
	currentComponent := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local == "rti" {
				continue
			}
			if typed.Name.Local == "installation" && currentComponent != "" {
				installation := rtiVersionInstallation{}
				if err := decoder.DecodeElement(&installation, &typed); err != nil {
					return nil, err
				}
				components = append(components, ComponentInstallation{
					Component:     currentComponent,
					Architecture:  installation.Architecture,
					Version:       installation.Version,
					Licensed:      installation.Licensed,
					FriendlyName:  installation.FriendlyName,
					InstallerName: installation.InstallerName,
				})
				continue
			}
			if currentComponent == "" {
				currentComponent = typed.Name.Local
			}
		case xml.EndElement:
			if typed.Name.Local == currentComponent {
				currentComponent = ""
			}
		}
	}
	return components, nil
}

func EnsureCollectorServiceLite(install Install, selectFunc func(message string, choices []string) (string, error), out io.Writer) error {
	return ensureCloudExtrasCapability(install, capabilityPatch{
		Name:        "RTI Collector Service Lite",
		CommandName: "gateway",
		Check:       HasCollectorServiceLite,
	}, selectFunc, out)
}

func EnsureEnhancedDDSSpy(install Install, selectFunc func(message string, choices []string) (string, error), out io.Writer) error {
	return ensureCloudExtrasCapability(install, capabilityPatch{
		Name:        "enhanced RTI DDS Spy",
		CommandName: "spy",
		Check:       HasEnhancedDDSSpy,
	}, selectFunc, out)
}

func ensureCloudExtrasCapability(install Install, patch capabilityPatch, selectFunc func(message string, choices []string) (string, error), out io.Writer) error {
	version := install.Version
	if version == "" {
		version = VersionFromPath(install.Path)
		install.Version = version
	}
	if CompareVersion(version, "7.7.0") < 0 {
		return common.UserError{Message: fmt.Sprintf("%s requires Connext Pro 7.7.0 with the Connext Cloud Extras package. Found Connext Pro %s at %s.", patch.Name, version, install.Path)}
	}
	if patch.Check(install) {
		return nil
	}
	if !isPatchableCloudExtrasVersion(version) {
		return common.UserError{Message: fmt.Sprintf("%s is missing from Connext Pro %s at %s.\nAutomatic installation of the Connext Cloud Extras package is available only for Connext Pro 7.7.0 installations. This is unexpected for Connext Pro %s.", patch.Name, version, install.Path, version)}
	}
	if selectFunc == nil {
		return common.UserError{Message: fmt.Sprintf("%s is missing from Connext Pro %s at %s.\nRun rticloud %s in an interactive terminal to install the Connext Cloud Extras package.", patch.Name, version, install.Path, patch.CommandName)}
	}
	message := fmt.Sprintf("%s is required for rticloud %s but is missing from Connext Pro %s at %s.\n\nInstall Connext Cloud Extras package to add this capability?", patch.Name, patch.CommandName, version, install.Path)
	selected, err := selectFunc(message, []string{InstallConnextCloudExtrasLabel, CancelPackageInstallLabel})
	if err != nil {
		return err
	}
	if selected != InstallConnextCloudExtrasLabel {
		return common.UserError{Message: "Connext Cloud Extras package installation cancelled."}
	}
	packagePath, err := DownloadCloudExtrasPackage(out)
	if err != nil {
		return err
	}
	if out == nil {
		out = io.Discard
	}
	_, _ = fmt.Fprintf(out, "Installing Connext Cloud Extras package from %s\n", packagePath)
	if err := PackageInstaller(install, packagePath, out); err != nil {
		return err
	}
	if !patch.Check(install) {
		return common.UserError{Message: fmt.Sprintf("Installed Connext Cloud Extras package from %s, but %s is still missing from %s.", packagePath, patch.Name, install.Path)}
	}
	_, _ = fmt.Fprintf(out, "Connext Cloud Extras package installed.\n")
	return nil
}

func DownloadCloudExtrasPackage(out io.Writer) (string, error) {
	url, err := cloudExtrasPackageURL()
	if err != nil {
		return "", err
	}
	workDir, err := CurrentWorkDir()
	if err != nil {
		return "", err
	}
	fileName := pathpkg.Base(url)
	targetPath := uniqueDownloadPath(filepath.Join(workDir, fileName))
	if out == nil {
		out = io.Discard
	}
	stopSpinner := terminal.StartSpinner(out, "Downloading Connext Cloud Extras package...")
	defer stopSpinner()
	if err := downloadFileWithDescription(url, targetPath, "Connext Cloud Extras package"); err != nil {
		return "", err
	}
	return targetPath, nil
}

func cloudExtrasPackageURL() (string, error) {
	goos, goarch := Platform()
	baseURL := fmt.Sprintf("https://s3.amazonaws.com/RTI/Bundles/%s/Cloud/rti_connext_dds-%s_RTI_ER_723", cloudExtrasBundleVersion, cloudExtrasBundleVersion)
	switch goos {
	case "linux":
		switch goarch {
		case "arm64":
			return baseURL + "-armv8Linux-cloud-extras.rtipkg", nil
		case "amd64":
			return baseURL + "-x64Linux-cloud-extras.rtipkg", nil
		}
	case "darwin":
		if goarch == "arm64" {
			return baseURL + "-arm64Darwin-cloud-extras.rtipkg", nil
		}
	case "windows":
		if goarch == "amd64" {
			return baseURL + "-x64Win64-cloud-extras.rtipkg", nil
		}
	}
	return "", common.UserError{Message: fmt.Sprintf("Automatic Connext Cloud Extras package download is not available for %s/%s.", goos, goarch)}
}

func isPatchableCloudExtrasVersion(version string) bool {
	parts := versionPartsRE.FindAllString(version, -1)
	if len(parts) < 3 {
		return false
	}
	return parts[0] == "7" && parts[1] == "7" && parts[2] == "0"
}

func isConnext770PatchRelease(version string) bool {
	match := versionRE.FindString(version)
	if match == "" {
		return false
	}
	parts := strings.Split(match, ".")
	return len(parts) == 4 && parts[0] == "7" && parts[1] == "7" && parts[2] == "0"
}

func hasConnext770PatchReleaseMetadata(installPath string) bool {
	components, err := InstalledComponents(installPath)
	if err != nil {
		return false
	}
	for _, component := range components {
		if isConnext770PatchRelease(component.Version) {
			return true
		}
	}
	return false
}

func runPackageInstaller(install Install, packagePath string, out io.Writer) error {
	executable := Executable(install.Path, "rtipkginstall")
	if _, err := os.Stat(executable); err != nil {
		return common.UserError{Message: fmt.Sprintf("rtipkginstall not found at %s.", executable)}
	}
	command := terminal.PrepareCommand([]string{executable, packagePath})
	cmd := exec.Command(command[0], command[1:]...)
	terminal.PrepareProcess(cmd)
	cmd.Dir = install.Path
	cmd.Stdin = strings.NewReader("y\n")
	if out == nil {
		out = io.Discard
	}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return common.UserError{Message: fmt.Sprintf("Connext Cloud Extras package installation failed: %v", err)}
	}
	return nil
}
