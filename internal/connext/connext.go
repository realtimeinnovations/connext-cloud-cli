package connext

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
)

type Install struct {
	Path    string
	Version string
	Reason  string
}

type DiscoveryOptions struct {
	MinVersion     string
	ExecutableName string
	CommandName    string
}

const (
	EnterConnextPathLabel        = "Enter Connext path"
	DownloadConnextLabel         = "Download Connext Professional"
	CancelConnextSelectionLabel  = "Cancel"
	nonStandardDirWarningMessage = "To use an installation in a non-standard directory, export NDDSHOME before."
	installerVersion             = "7.7.0"
)

var (
	InstallPatterns = []string{
		"/Applications/rti_connext_dds-*",
		"/opt/rti.com/rti_connext_dds-*",
		`C:\Program Files\rti_connext_dds-*`,
	}
	Glob           = filepath.Glob
	HTTPGet        = http.Get
	CurrentWorkDir = os.Getwd
	Platform       = func() (string, string) { return runtime.GOOS, runtime.GOARCH }
	versionRE      = regexp.MustCompile(`(\d+\.\d+\.\d+)`)
)

func ParseVersion(version string) []int {
	parts := regexp.MustCompile(`\d+`).FindAllString(version, 3)
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

func Executable(path string, executableName string) string {
	name := executableName
	if os.PathSeparator == '\\' && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		name += ".exe"
	}
	return filepath.Join(path, "bin", name)
}

func ValidateInstall(path string, options DiscoveryOptions) (Install, error) {
	options = normalizeOptions(options)
	resolved, err := filepath.Abs(path)
	if err != nil {
		return Install{}, err
	}
	version := VersionFromPath(resolved)
	if _, err := os.Stat(Executable(resolved, options.ExecutableName)); err != nil {
		return Install{}, common.UserError{Message: fmt.Sprintf("%s\n\nSet NDDSHOME to your Connext installation and rerun:\n  export NDDSHOME=/path/to/rti_connext_dds-%s\n  rticloud %s", missingInstallTitle(options), options.MinVersion, options.CommandName)}
	}
	if CompareVersion(version, options.MinVersion) < 0 {
		return Install{}, common.UserError{Message: fmt.Sprintf("Found Connext Pro %s at %s.\nrticloud %s requires Connext Pro %s or newer.", version, resolved, options.CommandName, options.MinVersion)}
	}
	return Install{Path: resolved, Version: version}, nil
}

func DiscoverInstall(env map[string]string, options DiscoveryOptions) (Install, error) {
	return DiscoverInstallWithPrompt(env, false, nil, nil, options)
}

func DiscoverInstallWithPrompt(env map[string]string, prompt bool, selectFunc func(message string, choices []string) (string, error), inputFunc func(message string) (string, error), options DiscoveryOptions) (Install, error) {
	options = normalizeOptions(options)
	if env == nil {
		env = map[string]string{}
		for _, item := range os.Environ() {
			parts := strings.SplitN(item, "=", 2)
			if len(parts) == 2 {
				env[parts[0]] = parts[1]
			}
		}
	}
	if home := env["NDDSHOME"]; home != "" {
		install, err := ValidateInstall(home, options)
		if err == nil {
			install.Reason = "selected via $NDDSHOME"
		}
		return install, err
	}
	if home := env["CONNEXTDDS_DIR"]; home != "" {
		install, err := ValidateInstall(home, options)
		if err == nil {
			install.Reason = "selected via $CONNEXTDDS_DIR"
		}
		return install, err
	}
	candidates := commonInstalls(options)
	if len(candidates) == 0 {
		if prompt && selectFunc != nil && inputFunc != nil {
			return resolveMissingInstall(selectFunc, inputFunc, options)
		}
		return Install{}, common.UserError{Message: missingInstallMessage(options)}
	}
	if len(candidates) == 1 {
		candidates[0].Reason = "only compatible installation found"
		return candidates[0], nil
	}
	if !prompt || selectFunc == nil {
		candidates[0].Reason = "highest version automatically selected"
		return candidates[0], nil
	}
	choices := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		choices = append(choices, candidate.Path)
	}
	selected, err := selectFunc("Select Connext installation:", choices)
	if err != nil {
		return Install{}, err
	}
	return ValidateInstall(selected, options)
}

func missingInstallMessage(options DiscoveryOptions) string {
	return fmt.Sprintf("%s\n\n%s\n\nSet NDDSHOME to your Connext installation and rerun:\n  export NDDSHOME=/path/to/rti_connext_dds-%s\n  rticloud %s", missingInstallTitle(options), nonStandardDirWarningMessage, options.MinVersion, options.CommandName)
}

func resolveMissingInstall(selectFunc func(message string, choices []string) (string, error), inputFunc func(message string) (string, error), options DiscoveryOptions) (Install, error) {
	message := fmt.Sprintf("%s\n\n%s\n\nSelect how to continue:", missingInstallTitle(options), nonStandardDirWarningMessage)
	for {
		selected, err := selectFunc(message, []string{EnterConnextPathLabel, DownloadConnextLabel, CancelConnextSelectionLabel})
		if err != nil {
			return Install{}, err
		}
		switch selected {
		case EnterConnextPathLabel:
			install, err := promptForInstallPath(inputFunc, options)
			if err == nil {
				return install, nil
			}
			return Install{}, err
		case DownloadConnextLabel:
			installerPath, err := DownloadInstaller()
			if err != nil {
				return Install{}, err
			}
			return Install{}, common.UserError{Message: fmt.Sprintf("Downloaded Connext Professional installer to:\n  %s\n\nRun the installer, then rerun:\n  rticloud %s", installerPath, options.CommandName)}
		case CancelConnextSelectionLabel:
			return Install{}, common.UserError{Message: "Connext selection cancelled."}
		default:
			return Install{}, common.UserError{Message: fmt.Sprintf("Unsupported selection: %s", selected)}
		}
	}
}

func missingInstallTitle(options DiscoveryOptions) string {
	return fmt.Sprintf("Connext Pro %s or newer with %s was not found.", options.MinVersion, options.ExecutableName)
}

func promptForInstallPath(inputFunc func(message string) (string, error), options DiscoveryOptions) (Install, error) {
	message := "Enter Connext installation path"
	for {
		value, err := inputFunc(message)
		if err != nil {
			return Install{}, err
		}
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			message = "Path cannot be empty.\n\nEnter Connext installation path"
			continue
		}
		install, err := ValidateInstall(trimmed, options)
		if err == nil {
			return install, nil
		}
		message = err.Error() + "\n\nEnter Connext installation path"
	}
}

func DownloadInstaller() (string, error) {
	url, err := installerURL()
	if err != nil {
		return "", err
	}
	workDir, err := CurrentWorkDir()
	if err != nil {
		return "", err
	}
	fileName := path.Base(url)
	targetPath := uniqueDownloadPath(filepath.Join(workDir, fileName))
	stopSpinner := terminal.StartSpinner(os.Stdout, "Downloading Connext Professional installer...")
	defer stopSpinner()
	if err := downloadFile(url, targetPath); err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.ToLower(targetPath), ".run") {
		_ = os.Chmod(targetPath, 0o755)
	}
	return targetPath, nil
}

func installerURL() (string, error) {
	goos, goarch := Platform()
	switch goos {
	case "linux":
		switch goarch {
		case "amd64":
			return fmt.Sprintf("https://s3.amazonaws.com/RTI/Bundles/%s/Evaluation/rti_connext_dds-%s-lm-x64Linux4gcc8.5.0.run", installerVersion, installerVersion), nil
		case "arm64":
			return fmt.Sprintf("https://s3.amazonaws.com/RTI/Bundles/%s/Evaluation/rti_connext_dds-%s-lm-armv8Linux4gcc8.5.0.run", installerVersion, installerVersion), nil
		}
	case "darwin":
		if goarch == "arm64" {
			return fmt.Sprintf("https://s3.amazonaws.com/RTI/Bundles/%s/Evaluation/rti_connext_dds-%s-lm-arm64Darwin23clang16.0.dmg", installerVersion, installerVersion), nil
		}
	case "windows":
		if goarch == "amd64" {
			return fmt.Sprintf("https://s3.amazonaws.com/RTI/Bundles/%s/Evaluation/rti_connext_dds-%s-lm-x64Win64VS2017.exe", installerVersion, installerVersion), nil
		}
	}
	return "", common.UserError{Message: fmt.Sprintf("Automatic Connext Professional download is not available for %s/%s.", goos, goarch)}
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
	response, err := HTTPGet(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return common.UserError{Message: fmt.Sprintf("Connext Professional download failed: %s", response.Status)}
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

func commonInstalls(options DiscoveryOptions) []Install {
	results := make([]Install, 0)
	seen := map[string]bool{}
	for _, pattern := range InstallPatterns {
		matches, err := Glob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			candidate, err := ValidateInstall(match, options)
			if err != nil || seen[candidate.Path] {
				continue
			}
			seen[candidate.Path] = true
			results = append(results, candidate)
		}
	}
	sort.Slice(results, func(i int, j int) bool {
		return CompareVersion(results[i].Version, results[j].Version) > 0
	})
	return results
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
