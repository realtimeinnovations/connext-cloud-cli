package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	connextInstallPatterns = []string{
		"/Applications/rti_connext_dds-*",
		"/opt/rti.com/rti_connext_dds-*",
		`C:\Program Files\rti_connext_dds-*`,
	}
	connextGlob = filepath.Glob
)

const MinConnextVersion = "7.3.0"

type GatewayError struct {
	Message string
}

func (err GatewayError) Error() string {
	return err.Message
}

func FormatAPIConnectionError(method string, apiHost string, path string, detail error) string {
	configuredHost := apiHost
	if strings.TrimSpace(configuredHost) == "" {
		configuredHost = "not configured"
	}
	return strings.Join([]string{
		"Cannot reach Connext Cloud API.",
		"",
		"Configured API host:",
		"  " + configuredHost,
		"",
		"The CLI could not connect to the configured management API.",
		"",
		"To use Connext Cloud:",
		"  rticloud configure --region us-west-2",
		"  rticloud login",
		"  rticloud gateway",
		"",
		"To use a local development API:",
		"  Configure the local API host, then start or port-forward the management API.",
		"",
		"Details:",
		fmt.Sprintf("  %s %s failed: %v", method, path, detail),
	}, "\n")
}

func DashboardURL(zone string, resourceName string, resourceKind string) string {
	host := ""
	scheme := "https"
	switch zone {
	case "dev-local":
		host = "localhost:8080"
		scheme = "http"
	case "dev-cloud":
		host = "test.cloud.dev-rti.com"
	default:
		host = zone + ".cloud.dev-rti.com"
	}
	path := "databuses"
	if resourceKind == "observability" {
		path = "telemetry-services"
	}
	return fmt.Sprintf("%s://%s/dashboard/%s/%s", scheme, host, path, resourceName)
}

func DashboardResourceKind(resourceLabel string) string {
	if resourceLabel == "Observability Service" {
		return "observability"
	}
	return "databus"
}

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

func compareVersion(left string, right string) int {
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

func RoutingExecutable(path string) string {
	name := "rtiroutingservice"
	if os.PathSeparator == '\\' {
		name = "rtiroutingservice.exe"
	}
	return filepath.Join(path, "bin", name)
}

func ValidateConnextInstall(path string) (ConnextInstall, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return ConnextInstall{}, err
	}
	version := VersionFromPath(resolved)
	if _, err := os.Stat(RoutingExecutable(resolved)); err != nil {
		return ConnextInstall{}, GatewayError{Message: fmt.Sprintf("Connext Pro %s or newer was not found.\n\nSet NDDSHOME to your Connext installation and rerun:\n  export NDDSHOME=/path/to/rti_connext_dds-%s\n  rticloud gateway", MinConnextVersion, MinConnextVersion)}
	}
	if compareVersion(version, MinConnextVersion) < 0 {
		return ConnextInstall{}, GatewayError{Message: fmt.Sprintf("Found Connext Pro %s at %s.\nrticloud gateway requires Connext Pro %s or newer.", version, resolved, MinConnextVersion)}
	}
	return ConnextInstall{Path: resolved, Version: version}, nil
}

func DiscoverConnextInstall(env map[string]string) (ConnextInstall, error) {
	return DiscoverConnextInstallWithPrompt(env, false, nil)
}

func DiscoverConnextInstallWithPrompt(env map[string]string, prompt bool, selectFunc func(message string, choices []string) (string, error)) (ConnextInstall, error) {
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
		return ValidateConnextInstall(home)
	}
	candidates := commonConnextInstalls()
	if len(candidates) == 0 {
		return ConnextInstall{}, GatewayError{Message: fmt.Sprintf("Connext Pro %s or newer was not found.\n\nSet NDDSHOME to your Connext installation and rerun:\n  export NDDSHOME=/path/to/rti_connext_dds-%s\n  rticloud gateway", MinConnextVersion, MinConnextVersion)}
	}
	if len(candidates) == 1 || !prompt {
		return candidates[0], nil
	}
	if selectFunc == nil {
		return candidates[0], nil
	}
	choices := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		choices = append(choices, candidate.Path)
	}
	selected, err := selectFunc("Select Connext installation:", choices)
	if err != nil {
		return ConnextInstall{}, err
	}
	return ValidateConnextInstall(selected)
}

func commonConnextInstalls() []ConnextInstall {
	results := make([]ConnextInstall, 0)
	seen := map[string]bool{}
	for _, pattern := range connextInstallPatterns {
		matches, err := connextGlob(pattern)
		if err != nil {
			continue
		}
		for _, match := range matches {
			candidate, err := ValidateConnextInstall(match)
			if err != nil || seen[candidate.Path] {
				continue
			}
			seen[candidate.Path] = true
			results = append(results, candidate)
		}
	}
	sort.Slice(results, func(i int, j int) bool {
		return compareVersion(results[i].Version, results[j].Version) > 0
	})
	return results
}
