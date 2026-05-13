package gateway

import (
	"fmt"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
)

const MinConnextVersion = "7.3.0"

type GatewayError = common.UserError

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
		path = "observability-services"
	}
	return fmt.Sprintf("%s://%s/dashboard/%s/%s", scheme, host, path, resourceName)
}

func DashboardResourceKind(resourceLabel string) string {
	if resourceLabel == "Observability Service" {
		return "observability"
	}
	return "databus"
}

func RoutingExecutable(path string) string {
	return connext.Executable(path, "rtiroutingservice")
}

func ValidateConnextInstall(path string) (ConnextInstall, error) {
	return connext.ValidateInstall(path, connextOptions())
}

func DiscoverConnextInstall(env map[string]string) (ConnextInstall, error) {
	return connext.DiscoverInstall(env, connextOptions())
}

func DiscoverConnextInstallWithPrompt(env map[string]string, prompt bool, selectFunc func(message string, choices []string) (string, error), inputFunc func(message string) (string, error)) (ConnextInstall, error) {
	return connext.DiscoverInstallWithPrompt(env, prompt, selectFunc, inputFunc, connextOptions())
}

func connextOptions() connext.DiscoveryOptions {
	return connext.DiscoveryOptions{MinVersion: MinConnextVersion, ExecutableName: "rtiroutingservice", CommandName: "gateway"}
}
