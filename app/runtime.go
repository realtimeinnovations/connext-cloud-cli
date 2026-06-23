package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/cloudapi"
	"github.com/realtimeinnovations/connext-cloud-cli/commands"
	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	mgcrypto "github.com/realtimeinnovations/connext-cloud-cli/crypto"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	internalconnext "github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/update"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

const (
	downloadConnextLicenseLabel = "Yes, download a license from evaluation.rti.com"
	cancelConnextLicenseLabel   = "No, skip license download"
)

type Runtime struct {
	Out      io.Writer
	Config   *config.Manager
	Auth     *auth.Manager
	WorkAuth *auth.Manager
	CloudAPI *cloudapi.Client
	Commands *commands.Runner
	License  *commands.Runner
	Gateway  *gateway.GatewayApp
	Spy      *spy.App
	Updater  *update.Manager
}

func NewRuntime(workDir string, out io.Writer) *Runtime {
	configManager := config.New("")
	authManager := auth.New(configManager, "")
	authManager.Stdout = out
	cloudClient := cloudapi.New(configManager.GetAPIURL, authManager.GetAuthHeaders)
	cloudClient.Out = out
	commandRunner := commands.New(cloudClient, out)
	commandRunner.CSRGenerator = mgcrypto.GeneratePrivateKeyAndCSR
	evaluationAuthManager := auth.NewEvaluationManager("")
	evaluationAuthManager.Stdout = out
	evaluationClient := cloudapi.New(auth.EvaluationAPIURL, evaluationAuthManager.GetAuthHeaders)
	evaluationClient.Out = out
	licenseRunner := commands.New(evaluationClient, out)
	gatewayApp := gateway.NewGatewayApp(workDir, out)
	gatewayApp.APIGet = func(path string) (map[string]any, error) {
		response, err := cloudClient.Get(path)
		return decodeCommandJSON(response, err, "GET", path, configManager.GetAPIURLSafe(), "gateway")
	}
	gatewayApp.APIPost = func(path string, payload map[string]any) (map[string]any, error) {
		response, err := cloudClient.Post(path, payload)
		return decodeCommandJSON(response, err, "POST", path, configManager.GetAPIURLSafe(), "gateway")
	}
	gatewayApp.CurrentZoneFunc = func() string { return currentZone(configManager) }
	gatewayApp.DiscoverConnextInstallFn = func(prompt bool) (gateway.ConnextInstall, error) {
		return gateway.DiscoverConnextInstallWithPrompt(nil, prompt, gatewayApp.SelectFunc, gatewayApp.InputFunc)
	}
	gatewayApp.GenerateCSRFunc = mgcrypto.GeneratePrivateKeyAndCSR
	spyApp := spy.NewApp(workDir, out)
	spyApp.APIGet = func(path string) (map[string]any, error) {
		response, err := cloudClient.Get(path)
		return decodeCommandJSON(response, err, "GET", path, configManager.GetAPIURLSafe(), "spy")
	}
	spyApp.APIPost = func(path string, payload map[string]any) (map[string]any, error) {
		response, err := cloudClient.Post(path, payload)
		return decodeCommandJSON(response, err, "POST", path, configManager.GetAPIURLSafe(), "spy")
	}
	spyApp.CurrentZoneFunc = func() string { return currentZone(configManager) }
	spyApp.DiscoverConnextInstallFn = func(prompt bool) (spy.ConnextInstall, error) {
		return spy.DiscoverConnextInstallWithPrompt(nil, prompt, spyApp.SelectFunc, spyApp.InputFunc)
	}
	spyApp.GenerateCSRFunc = mgcrypto.GeneratePrivateKeyAndCSR
	updater := update.New(configManager, out)
	return &Runtime{Out: out, Config: configManager, Auth: authManager, WorkAuth: evaluationAuthManager, CloudAPI: cloudClient, Commands: commandRunner, License: licenseRunner, Gateway: gatewayApp, Spy: spyApp, Updater: updater}
}

func (runtime *Runtime) Logout() error {
	if runtime.Auth != nil {
		if err := runtime.Auth.Logout(); err != nil {
			return err
		}
	}
	if runtime.WorkAuth != nil {
		if err := runtime.WorkAuth.Logout(); err != nil {
			return err
		}
	}
	return nil
}

func decodeCommandJSON(response *http.Response, err error, method string, path string, apiHost string, command string) (map[string]any, error) {
	if err != nil {
		if errors.Is(err, config.ErrNotConfigured) {
			return nil, gateway.GatewayError{Message: err.Error()}
		}
		message := gateway.FormatAPIConnectionError(method, apiHost, path, err)
		if command != "gateway" {
			message = strings.ReplaceAll(message, "rticloud gateway", "rticloud "+command)
		}
		return nil, gateway.GatewayError{Message: message}
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return nil, gateway.GatewayError{Message: "Error: " + httputil.FormatError(response.StatusCode, body)}
	}
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func currentZone(manager *config.Manager) string {
	configValues, err := manager.GetConfig()
	if err != nil {
		return "unknown"
	}
	apiHost := configValues["api_host"]
	for zone, url := range config.RegionURLMap {
		if apiHost == url {
			return zone
		}
	}
	if apiHost == "" {
		return "unknown"
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(apiHost, "https://"), "http://")
	trimmed = strings.SplitN(trimmed, ".", 2)[0]
	trimmed = strings.SplitN(trimmed, ":", 2)[0]
	return trimmed
}

func (runtime *Runtime) RunSpy(format string, skipPreflight bool) error {
	configValues, err := runtime.Spy.ReadConfig()
	if err != nil {
		return err
	}
	existingConfig := configValues != nil
	if configValues == nil {
		if skipPreflight {
			return common.UserError{Message: "No spy configuration found in this project.\n\nRun without --skip-preflight to configure this project:\n  rticloud spy"}
		}
		configValues, err = runtime.Spy.ConfigureFirstRun(!runtime.liveTextOutput(format))
		if err != nil {
			return err
		}
	}
	runtime.Spy.PrintConfigSummary(configValues)
	databusSecure := false
	if skipPreflight {
		if err := runtime.Spy.ValidateLocalArtifacts(configValues); err != nil {
			return err
		}
		databusSecure = runtime.Spy.LocalSecureArtifacts()
	} else {
		if existingConfig {
			_, _ = fmt.Fprint(runtime.Out, spy.RenderInfoMessage("Checking service status. To skip this check, rerun with --skip-preflight."))
		}
		if err := runtime.Spy.ValidateConfigResources(configValues); err != nil {
			return err
		}
		if err := runtime.Spy.DownloadArtifacts(configValues, false); err != nil {
			return err
		}
		databusSecure, err = runtime.Spy.EnsureSecureArtifacts(configValues)
		if err != nil {
			return err
		}
	}
	runtimeConfig, _ := configValues["runtime"].(map[string]any)
	connextHome, _ := runtimeConfig["connext_home"].(string)
	var connext spy.ConnextInstall
	if connextHome != "" {
		connext, err = spy.ValidateConnextInstall(connextHome)
	} else {
		connext, err = spy.DiscoverConnextInstall(nil)
	}
	if err != nil {
		return err
	}
	if err := runtime.ensureConnextLicense(connext, runtime.Spy.SelectFunc); err != nil {
		return err
	}
	if err := spy.EnsureEnhancedDDSSpy(connext, runtime.Spy.SelectFunc, runtime.Out); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runtime.Out, "Connext Pro %s found at %s\n", connext.Version, connext.Path)
	_, err = runtime.Spy.RunWithOptions(configValues, connext, databusSecure, spy.RunOptions{TextOutput: runtime.liveTextOutput(format)})
	return err
}

func (runtime *Runtime) RunGateway(format string, skipPreflight bool) error {
	configValues, err := runtime.Gateway.ReadConfig()
	if err != nil {
		return err
	}
	existingConfig := configValues != nil
	if configValues == nil {
		if skipPreflight {
			return gateway.GatewayError{Message: "No gateway configuration found in this project.\n\nRun without --skip-preflight to configure this project:\n  rticloud gateway"}
		}
		configValues, err = runtime.Gateway.ConfigureFirstRun(!runtime.liveTextOutput(format))
		if err != nil {
			return err
		}
	}
	runtime.Gateway.PrintConfigSummary(configValues)
	databusSecure := false
	collectorSecure := false
	if skipPreflight {
		if err := runtime.Gateway.ValidateLocalArtifacts(configValues); err != nil {
			return err
		}
		databusSecure, collectorSecure = runtime.Gateway.LocalSecureArtifacts()
	} else {
		if existingConfig {
			_, _ = fmt.Fprint(runtime.Out, gateway.RenderInfoMessage("Checking service status. To skip this check, rerun with --skip-preflight."))
		}
		if err := runtime.Gateway.ValidateConfigResources(configValues); err != nil {
			return err
		}
		if err := runtime.Gateway.DownloadArtifacts(configValues, false); err != nil {
			return err
		}
		databusSecure, collectorSecure, err = runtime.Gateway.EnsureSecureArtifacts(configValues)
		if err != nil {
			return err
		}
	}
	runtimeConfig, _ := configValues["runtime"].(map[string]any)
	connextHome, _ := runtimeConfig["connext_home"].(string)
	var connext gateway.ConnextInstall
	if gateway.HasDatabus(configValues) {
		if connextHome != "" {
			connext, err = gateway.ValidateConnextInstall(connextHome)
		} else {
			connext, err = gateway.DiscoverConnextInstall(nil)
		}
		if err != nil {
			return err
		}
		if err := runtime.ensureConnextLicense(connext, runtime.Gateway.SelectFunc); err != nil {
			return err
		}
		if gateway.HasObservability(configValues) {
			if err := gateway.EnsureCollectorServiceLite(connext, runtime.Gateway.SelectFunc, runtime.Out); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(runtime.Out, "Connext Pro %s found at %s\n", connext.Version, connext.Path)
		// Start the collector as a background process alongside the routing service.
		collectorPID := 0
		if gateway.HasObservability(configValues) {
			collectorCmd, startErr := runtime.Gateway.StartCollector(configValues, connext, collectorSecure)
			if startErr != nil {
				return startErr
			}
			collectorPID = collectorCmd.Process.Pid
			defer func() {
				if collectorCmd.Process != nil {
					terminal.KillProcess(collectorCmd.Process)
				}
			}()
		}
		_, err = runtime.Gateway.RunRoutingServiceWithOptions(configValues, connext, collectorPID, databusSecure, collectorSecure, gateway.RunOptions{TextOutput: runtime.liveTextOutput(format)})
		return err
	}
	// Observability-only: discover a base Connext install, patch collector lite if needed, and run it in the foreground.
	if gateway.HasObservability(configValues) {
		if connextHome != "" {
			connext, err = gateway.ValidateConnextInstall(connextHome)
		} else {
			connext, err = gateway.DiscoverConnextInstall(nil)
		}
		if err != nil {
			return err
		}
		if err := runtime.ensureConnextLicense(connext, runtime.Gateway.SelectFunc); err != nil {
			return err
		}
		if err := gateway.EnsureCollectorServiceLite(connext, runtime.Gateway.SelectFunc, runtime.Out); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(runtime.Out, "Connext Pro %s found at %s\n", connext.Version, connext.Path)
		_, err = runtime.Gateway.RunCollectorServiceWithOptions(configValues, connext, collectorSecure, gateway.RunOptions{TextOutput: runtime.liveTextOutput(format)})
		return err
	}
	return nil
}

func (runtime *Runtime) liveTextOutput(format string) bool {
	return format == "text" || terminal.PlainOutputRequested(runtime.Out)
}

func (runtime *Runtime) ensureConnextLicense(install internalconnext.Install, selectFunc func(message string, choices []string) (string, error)) error {
	if !internalconnext.IsLicenseManaged(install) || internalconnext.HasLicenseAvailable(install) {
		return nil
	}
	if selectFunc == nil {
		return common.UserError{Message: fmt.Sprintf("Connext Pro at %s is license-managed and no license file was found. Run rticloud in an interactive terminal to download a license from evaluation.rti.com.", install.Path)}
	}
	message := fmt.Sprintf("Connext Pro at %s is license-managed, but no license file was found.\n\nDownload a license from evaluation.rti.com now?", install.Path)
	selected, err := selectFunc(message, []string{downloadConnextLicenseLabel, cancelConnextLicenseLabel})
	if err != nil {
		return err
	}
	if selected != downloadConnextLicenseLabel {
		return common.UserError{Message: "Connext license download cancelled."}
	}
	if runtime.License == nil {
		return fmt.Errorf("Connext license download is not configured")
	}
	licenseContent, err := runtime.License.DownloadLicense(nil)
	if err != nil {
		return common.UserError{Message: fmt.Sprintf("Connext license download failed: %v", err)}
	}
	if err := internalconnext.WriteLicenseFile(install, licenseContent); err != nil {
		return fmt.Errorf("saving Connext license to %s: %w", internalconnext.LicenseFilePath(install), err)
	}
	_, _ = fmt.Fprintf(runtime.Out, "Connext license saved to %s\n", internalconnext.LicenseFilePath(install))
	return nil
}
