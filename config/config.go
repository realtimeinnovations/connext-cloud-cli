package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/buildinfo"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

var RegionURLMap = map[string]string{
	"us-east-2":    "https://cloud.rti.com/api/v1",
	"eu-central-1": "https://eu-central-1.cloud.rti.com/api/v1",
	"dev-cloud":    "https://test.cloud.dev-rti.com/api/v1",
	"dev-local":    "http://localhost:8090",
}

var standardRegionOrder = []string{"us-east-2", "eu-central-1"}

var defaultClientID = ""
var defaultWorkspacesClientID = ""

const previewWarning = "⚠ Connext Cloud is in preview. Do not use in production."
const customCloudDomainChoice = "__custom_cloud_domain__"

const NotConfiguredMessage = "RTI Connext Cloud CLI not configured.\n\nFirst run:\n  rticloud configure\n  rticloud login"

var ErrNotConfigured = errors.New(NotConfiguredMessage)

type Manager struct {
	Path  string
	Env   func(string) string
	cache map[string]string
}

func DefaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rticloud")
}

func DefaultConfigPath() string {
	return filepath.Join(DefaultDir(), "config.json")
}

func DefaultCredentialsPath() string {
	return filepath.Join(DefaultDir(), "credentials.json")
}

func New(path string) *Manager {
	if path == "" {
		path = DefaultConfigPath()
	}
	return &Manager{Path: path, Env: os.Getenv}
}

func (manager *Manager) GetConfig() (map[string]string, error) {
	if manager.cache != nil {
		return copyMap(manager.cache), nil
	}
	data, err := os.ReadFile(manager.Path)
	if err != nil {
		if os.IsNotExist(err) {
			manager.cache = map[string]string{}
			return map[string]string{}, nil
		}
		return nil, err
	}
	config := map[string]string{}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, err
		}
	}
	manager.cache = config
	return copyMap(config), nil
}

func (manager *Manager) WriteConfig(config map[string]string) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manager.Path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(manager.Path, data, 0o600); err != nil {
		return err
	}
	manager.cache = copyMap(config)
	return nil
}

func (manager *Manager) GetAPIURL() (string, error) {
	if !manager.IsConfigured() {
		return "", ErrNotConfigured
	}
	config, err := manager.GetConfig()
	if err != nil {
		return "", err
	}
	return config["api_host"], nil
}

func (manager *Manager) GetAPIURLSafe() string {
	config, err := manager.GetConfig()
	if err != nil {
		return ""
	}
	if apiHost := config["api_host"]; apiHost != "" {
		return apiHost
	}
	return ""
}

func (manager *Manager) GetClientID() string {
	if manager.Env != nil {
		if value := manager.Env("CONNEXT_CLOUD_CLI_CLIENT_ID"); value != "" {
			return value
		}
	}
	config, err := manager.GetConfig()
	if err == nil {
		if value := config["auth0_client_id"]; value != "" {
			return value
		}
	}
	return defaultClientID
}

func GetWorkspacesClientID(env func(string) string) string {
	if env != nil {
		if value := env("CONNEXT_WORKSPACES_CLI_CLIENT_ID"); value != "" {
			return value
		}
		if value := env("WORKSPACES_AUTH0_CLIENT_ID"); value != "" {
			return value
		}
	}
	return defaultWorkspacesClientID
}

func (manager *Manager) IsConfigured() bool {
	_, err := os.Stat(manager.Path)
	if err != nil {
		return false
	}
	config, err := manager.GetConfig()
	if err != nil {
		return false
	}
	apiHost := config["api_host"]
	return apiHost != ""
}

func (manager *Manager) RequireConfiguration(out io.Writer) bool {
	if manager.IsConfigured() {
		return true
	}
	_, _ = fmt.Fprintln(out, NotConfiguredMessage)
	return false
}

func (manager *Manager) ConfigureRegion(region string, getRegion bool, in io.Reader, out io.Writer) (bool, error) {
	currentConfig, err := manager.GetConfig()
	if err != nil {
		return false, err
	}
	if getRegion {
		currentAPIHost := manager.GetAPIURLSafe()
		if currentAPIHost == "" {
			_, _ = fmt.Fprintln(out, "Current region: not configured")
			return true, nil
		}
		for configuredRegion, url := range RegionURLMap {
			if currentAPIHost == url {
				_, _ = fmt.Fprintf(out, "Current region: %s\n", configuredRegion)
				return true, nil
			}
		}
		_, _ = fmt.Fprintln(out, "Current region: custom (not using standard regions)")
		_, _ = fmt.Fprintf(out, "Current API host: %s\n", currentAPIHost)
		return true, nil
	}
	if region == "" {
		input := in
		if input == nil {
			input = os.Stdin
		}
		promptInput := input
		if _, _, ok := terminal.PromptFiles(input, out); !ok {
			promptInput = bufio.NewReader(input)
		}
		defaultRegion := "us-east-2"
		currentAPIHost := manager.GetAPIURLSafe()
		for configuredRegion, url := range RegionURLMap {
			if currentAPIHost != url {
				continue
			}
			if strings.HasPrefix(configuredRegion, "dev-") {
				break
			}
			defaultRegion = configuredRegion
			break
		}
		_, _ = fmt.Fprint(out, renderConfigureWelcome(out))
		selectedRegion, err := prompt.Selector{
			In:            promptInput,
			Out:           out,
			CancelMessage: "Configuration cancelled.",
			DefaultChoice: defaultRegion,
		}.Select("Select region:", interactiveRegionChoices())
		if err != nil {
			return false, err
		}
		region = selectedRegion
		if region == customCloudDomainChoice {
			domain, err := prompt.Input{
				In:            promptInput,
				Out:           out,
				CancelMessage: "Configuration cancelled.",
			}.Prompt("Enter full cloud domain (for example, my-region.cloud.rti.com)")
			if err != nil {
				return false, err
			}
			apiHost, err := customDomainAPIHost(domain)
			if err != nil {
				_, _ = fmt.Fprintf(out, "Error: %v\n", err)
				return false, nil
			}
			currentConfig["api_host"] = apiHost
			if err := manager.WriteConfig(currentConfig); err != nil {
				_, _ = fmt.Fprintf(out, "Error updating configuration: %v\n", err)
				return false, nil
			}
			_, _ = fmt.Fprintln(out, "Configuration updated. Run rticloud login or export CONNEXT_CLOUD_API_KEY.")
			return true, nil
		}
	} else {
		_, _ = fmt.Fprintf(out, "%s\n\n", previewWarning)
	}
	if _, ok := RegionURLMap[region]; !ok {
		_, _ = fmt.Fprintf(out, "Error: Invalid region '%s'. Available regions: %s\n", region, strings.Join(standardRegions(), ", "))
		return false, nil
	}
	currentConfig["api_host"] = RegionURLMap[region]
	if err := manager.WriteConfig(currentConfig); err != nil {
		_, _ = fmt.Fprintf(out, "Error updating configuration: %v\n", err)
		return false, nil
	}
	_, _ = fmt.Fprintln(out, "Configuration updated. Run rticloud login or export CONNEXT_CLOUD_API_KEY.")
	return true, nil
}

func renderConfigureWelcome(out io.Writer) string {
	width, _ := tui.TerminalSize(out, 76, 24)
	body := []string{
		tui.StyleSection("Welcome!"),
		previewWarning,
		"",
		tui.Dim(buildinfo.VersionLine()),
	}
	return strings.Join(tui.RenderPanel("RTI Connext Cloud", body, tui.MinInt(width, 76), configurePanelTheme()), "\n") + "\n\n"
}

func configurePanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleTitle, BorderStyle: tui.StyleBlueBorder, PaddedBody: true}
}

func standardRegions() []string {
	regions := make([]string, 0, len(RegionURLMap))
	seen := map[string]bool{}
	for _, configuredRegion := range standardRegionOrder {
		if _, ok := RegionURLMap[configuredRegion]; ok {
			regions = append(regions, configuredRegion)
			seen[configuredRegion] = true
		}
	}
	extraRegions := []string{}
	for configuredRegion := range RegionURLMap {
		if seen[configuredRegion] {
			continue
		}
		if strings.HasPrefix(configuredRegion, "dev-") {
			continue
		}
		extraRegions = append(extraRegions, configuredRegion)
	}
	sort.Strings(extraRegions)
	return append(regions, extraRegions...)
}

func interactiveRegionChoices() []string {
	regions := standardRegions()
	choices := make([]string, 0, len(regions)+1)
	for _, region := range regions {
		choices = append(choices, prompt.ChoiceWithLabel(region, regionChoiceLabel(region)))
	}
	return append(choices, prompt.ChoiceWithLabel(customCloudDomainChoice, "Custom domain\nEnter the full domain by hand"))
}

func regionChoiceLabel(region string) string {
	return region + "\n" + regionDomain(region)
}

func regionDomain(region string) string {
	apiHost := RegionURLMap[region]
	parsed, err := url.Parse(apiHost)
	if err == nil && parsed.Host != "" {
		return parsed.Host
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(apiHost, "https://"), "http://")
	return strings.SplitN(trimmed, "/", 2)[0]
}

func customDomainAPIHost(value string) (string, error) {
	domain := strings.TrimSpace(value)
	if parsed, err := url.Parse(domain); err == nil && parsed.Host != "" {
		domain = parsed.Host
	}
	domain = strings.Trim(strings.SplitN(domain, "/", 2)[0], ".")
	if domain == "" {
		return "", errors.New("cloud domain is required")
	}
	if strings.ContainsAny(domain, " \t\r\n") {
		return "", fmt.Errorf("invalid cloud domain %q", value)
	}
	return "https://" + domain + "/api/v1", nil
}

func copyMap(input map[string]string) map[string]string {
	output := map[string]string{}
	for key, value := range input {
		output[key] = value
	}
	return output
}
