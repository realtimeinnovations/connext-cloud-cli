package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/buildinfo"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

var RegionURLMap = map[string]string{
	"us-west-2":    "https://us-west-2.cloud.dev-rti.com/api/v1",
	"eu-central-1": "https://eu-central-1.cloud.dev-rti.com/api/v1",
	"dev-cloud":    "https://test.cloud.dev-rti.com/api/v1",
	"dev-local":    "http://localhost:8090",
}

var defaultClientID = ""

const previewWarning = "⚠ Connext Cloud is in preview. Do not use in production."

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
	return defaultClientID
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
		defaultRegion := "us-west-2"
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
			In:            in,
			Out:           out,
			CancelMessage: "Configuration cancelled.",
			DefaultChoice: defaultRegion,
		}.Select("Select region:", standardRegions())
		if err != nil {
			return false, err
		}
		region = selectedRegion
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
	for configuredRegion := range RegionURLMap {
		if strings.HasPrefix(configuredRegion, "dev-") {
			continue
		}
		regions = append(regions, configuredRegion)
	}
	sort.Strings(regions)
	return regions
}

func copyMap(input map[string]string) map[string]string {
	output := map[string]string{}
	for key, value := range input {
		output[key] = value
	}
	return output
}
