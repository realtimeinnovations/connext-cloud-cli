// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
)

type Install struct {
	Path    string
	Version string
	Reason  string
}

type DiscoveryOptions struct {
	Context                context.Context
	Output                 io.Writer
	HTTPClient             *http.Client
	MinVersion             string
	ExecutableName         string
	CommandName            string
	AcceptExternalNDDSHome bool
	Confirmations          Confirmer
}

func nddshomeSetCommand(minVersion string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(
			"  PowerShell:  $env:NDDSHOME = \"C:\\path\\to\\rti_connext_dds-%s\"\n  cmd.exe:     set NDDSHOME=C:\\path\\to\\rti_connext_dds-%s",
			minVersion, minVersion)
	}
	return fmt.Sprintf("  export NDDSHOME=/path/to/rti_connext_dds-%s", minVersion)
}

var (
	UserHomeDir    = os.UserHomeDir
	HTTPGet        = http.Get
	CurrentWorkDir = os.Getwd
	Platform       = func() (string, string) { return runtime.GOOS, runtime.GOARCH }
	versionRE      = regexp.MustCompile(`(\d+\.\d+\.\d+(?:\.\d+)?)`)
)

func ParseVersion(version string) []int {
	parts := regexp.MustCompile(`\d+`).FindAllString(version, -1)
	if len(parts) == 0 {
		return []int{0}
	}
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		var value int
		fmt.Sscanf(part, "%d", &value)
		out = append(out, value)
	}
	return out
}

func CompareVersion(left string, right string) int {
	leftParts := ParseVersion(left)
	rightParts := ParseVersion(right)
	maxLen := len(leftParts)
	if len(rightParts) > maxLen {
		maxLen = len(rightParts)
	}
	for idx := 0; idx < maxLen; idx++ {
		leftValue := 0
		rightValue := 0
		if idx < len(leftParts) {
			leftValue = leftParts[idx]
		}
		if idx < len(rightParts) {
			rightValue = rightParts[idx]
		}
		if leftValue < rightValue {
			return -1
		}
		if leftValue > rightValue {
			return 1
		}
	}
	return 0
}

func VersionFromPath(path string) string {
	match := versionRE.FindStringSubmatch(path)
	if match == nil {
		return "0.0.0"
	}
	return match[1]
}

func Executable(installPath string, executableName string) string {
	if os.PathSeparator == '\\' {
		// On Windows, Connext ships .bat launchers rather than native .exe binaries.
		// Try .bat first (preferred), then .exe.
		for _, ext := range []string{".bat", ".exe"} {
			candidate := filepath.Join(installPath, "bin", executableName+ext)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
		// Neither found — return the .bat path so error messages show a clear expectation.
		return filepath.Join(installPath, "bin", executableName+".bat")
	}
	return filepath.Join(installPath, "bin", executableName)
}

func ValidateInstall(path string, options DiscoveryOptions) (Install, error) {
	options = normalizeOptions(options)
	resolved, err := filepath.Abs(path)
	if err != nil {
		return Install{}, err
	}
	version := VersionFromPath(filepath.Base(resolved))
	// The directory name omits the patch version; metadata is authoritative.
	if host, err := readInstallHost(resolved); err == nil && exactVersionRE.MatchString(host.Version) {
		version = host.Version
	}
	executable := Executable(resolved, options.ExecutableName)
	if _, err := os.Stat(executable); err != nil {
		return Install{}, common.UserError{Message: fmt.Sprintf("%s\n\nExpected executable not found:\n  %s\n\nSet NDDSHOME to your Connext installation and rerun:\n%s\n  rticloud %s", missingInstallTitle(options), executable, nddshomeSetCommand(options.MinVersion), options.CommandName)}
	}
	if CompareVersion(version, options.MinVersion) < 0 {
		return Install{}, common.UserError{Message: fmt.Sprintf("Found Connext Pro %s at %s.\nrticloud %s requires Connext Pro %s or newer.", version, resolved, options.CommandName, options.MinVersion)}
	}
	return Install{Path: resolved, Version: version}, nil
}

// DiscoverInstall uses managed Connext unless the user confirms NDDSHOME.
func DiscoverInstall(env map[string]string, options DiscoveryOptions) (Install, error) {
	options = normalizeOptions(options)
	home := ""
	if env == nil {
		home = os.Getenv("NDDSHOME")
	} else {
		home = env["NDDSHOME"]
	}
	if home != "" {
		managed, err := ManagedInstallationPath()
		// External installations remain usable on platforms without a bundled installer.
		if err != nil || !sameInstallationPath(home, managed) {
			useOverride := options.AcceptExternalNDDSHome
			if !useOverride {
				if options.Confirmations == nil {
					return Install{}, common.UserError{Message: "NDDSHOME points to an external Connext installation. Run interactively to confirm it, unset NDDSHOME to use the rticloud-managed Connext installation, or use --skip-preflight to accept NDDSHOME."}
				}
				answer, err := options.Confirmations.Confirm(Confirmation{Message: fmt.Sprintf("NDDSHOME is set to %s.\n\nUse this installation instead of rticloud-managed Connext under your .rti directory?", home), DeclineLabel: UseManagedConnextLabel, AcceptLabel: UseNDDSHOMELabel})
				if err != nil {
					return Install{}, err
				}
				useOverride = answer
			}
			if useOverride {
				install, err := ValidateInstall(home, options)
				if err == nil {
					install.Reason = "selected via $NDDSHOME"
				}
				return install, err
			}
		}
	}
	return ManagedInstaller(options)
}

const (
	UseManagedConnextLabel = "No, use rticloud-managed Connext [recommended]"
	UseNDDSHOMELabel       = "Yes, use NDDSHOME"
)

func ManagedInstallationPath() (string, error) {
	root, err := managedRoot()
	if err != nil {
		return "", err
	}
	artifact, err := bundledInstaller(Platform())
	if err != nil {
		return "", err
	}
	return artifact.installationPath(root), nil
}

func sameInstallationPath(left, right string) bool {
	canonical := func(value string) string {
		absolute, err := filepath.Abs(value)
		if err == nil {
			value = absolute
		}
		if resolved, err := filepath.EvalSymlinks(value); err == nil {
			value = resolved
		}
		return filepath.Clean(value)
	}
	left, right = canonical(left), canonical(right)
	platform, _ := Platform()
	if platform == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func missingInstallTitle(options DiscoveryOptions) string {
	return fmt.Sprintf("Connext Pro %s or newer with %s was not found.", options.MinVersion, options.ExecutableName)
}

func uniqueDownloadPath(targetPath string) string {
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		return targetPath
	}
	ext := filepath.Ext(targetPath)
	base := strings.TrimSuffix(targetPath, ext)
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%d%s", base, index, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func downloadFile(url string, targetPath string) error {
	return downloadFileWithDescription(url, targetPath, "Connext Professional")
}

func downloadFileWithDescription(url string, targetPath string, description string) error {
	response, err := HTTPGet(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return common.UserError{Message: fmt.Sprintf("%s download failed: %s", description, response.Status)}
	}
	file, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(file, response.Body); err != nil {
		return err
	}
	return nil
}

func normalizeOptions(options DiscoveryOptions) DiscoveryOptions {
	if options.MinVersion == "" {
		options.MinVersion = "7.3.0"
	}
	if options.ExecutableName == "" {
		options.ExecutableName = "rtiroutingservice"
	}
	if options.CommandName == "" {
		options.CommandName = "gateway"
	}
	return options
}
