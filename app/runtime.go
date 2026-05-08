package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/cli"
	"github.com/realtimeinnovations/connext-cloud-cli/cloudapi"
	"github.com/realtimeinnovations/connext-cloud-cli/commands"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	mgcrypto "github.com/realtimeinnovations/connext-cloud-cli/crypto"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

type Runtime struct {
	Out      io.Writer
	Config   *config.Manager
	Auth     *auth.Manager
	CloudAPI *cloudapi.Client
	Commands *commands.Runner
	Gateway  *gateway.GatewayApp
	Spy      *spy.App
}

func NewRuntime(workDir string, out io.Writer) *Runtime {
	configManager := config.New("")
	authManager := auth.New(configManager, "")
	authManager.Stdout = out
	cloudClient := cloudapi.New(configManager.GetAPIURL, authManager.GetAuthHeaders)
	cloudClient.Out = out
	commandRunner := commands.New(cloudClient, out)
	commandRunner.CSRGenerator = mgcrypto.GeneratePrivateKeyAndCSR
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
	return &Runtime{Out: out, Config: configManager, Auth: authManager, CloudAPI: cloudClient, Commands: commandRunner, Gateway: gatewayApp, Spy: spyApp}
}

func decodeGatewayJSON(response *http.Response, err error, method string, path string, apiHost string) (map[string]any, error) {
	return decodeCommandJSON(response, err, method, path, apiHost, "gateway")
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

func (runtime *Runtime) Execute(args cli.Args) error {
	runtime.CloudAPI.SSLVerify = !args.DisableSSLVerify
	switch args.Resource {
	case "version":
		_, _ = fmt.Fprint(runtime.Out, VersionString())
		return nil
	case "configure":
		_, err := runtime.Config.ConfigureRegion(args.Region, args.GetRegion, os.Stdin, runtime.Out)
		return err
	case "login":
		_, err := runtime.Auth.Login()
		return err
	case "logout":
		return runtime.Auth.Logout()
	case "databus":
		return runtime.executeDatabus(args)
	case "observability":
		return runtime.executeObservability(args)
	case "client":
		return runtime.executeClient(args)
	case "app-client":
		return runtime.executeAppClient(args)
	case "network":
		return runtime.executeNetwork(args)
	case "license":
		return runtime.executeLicense(args)
	case "gateway":
		return runtime.executeGateway(args)
	case "spy":
		return runtime.executeSpy(args)
	default:
		return fmt.Errorf("unsupported resource: %s", args.Resource)
	}
}

func (runtime *Runtime) executeSpy(args cli.Args) error {
	switch args.SpyCommand {
	case "status":
		return runtime.Spy.Status()
	case "reset":
		return runtime.Spy.Reset()
	default:
		configValues, err := runtime.Spy.ReadConfig()
		if err != nil {
			return err
		}
		if configValues == nil {
			configValues, err = runtime.Spy.ConfigureFirstRun(!runtime.liveTextOutput(args))
			if err != nil {
				return err
			}
		}
		runtime.Spy.PrintConfigSummary(configValues)
		if err := runtime.Spy.ValidateConfigResources(configValues); err != nil {
			return err
		}
		if err := runtime.Spy.DownloadArtifacts(configValues, false); err != nil {
			return err
		}
		databusSecure, err := runtime.Spy.EnsureSecureArtifacts(configValues)
		if err != nil {
			return err
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
		_, _ = fmt.Fprintf(runtime.Out, "Connext Pro %s found at %s\n", connext.Version, connext.Path)
		_, err = runtime.Spy.RunWithOptions(configValues, connext, databusSecure, spy.RunOptions{TextOutput: runtime.liveTextOutput(args)})
		return err
	}
}

func (runtime *Runtime) executeDatabus(args cli.Args) error {
	switch args.Command {
	case "create":
		return runtime.Commands.CreateDatabus(args.Name, args.Replicas, args.ObservabilityService, args.SystemDesigner, args.NetworkName)
	case "list":
		return runtime.Commands.ListDatabuses(args.Short)
	case "query":
		return runtime.Commands.QueryDatabus(args.Name)
	case "delete":
		return runtime.Commands.DeleteDatabus(args.Name)
	case "disable":
		return runtime.Commands.UpdateDatabusStatus(args.Name, "disable")
	case "resume":
		return runtime.Commands.UpdateDatabusStatus(args.Name, "resume")
	case "set-observability":
		var service any = args.Service
		if args.Unlink {
			service = nil
		}
		return runtime.Commands.UpdateObservabilityLink(args.Name, service)
	case "update-filters":
		return runtime.Commands.UpdateFilters(args.Name, args.Filters)
	case "add-user":
		return runtime.Commands.AddUserToDatabus(args.Name, args.Email)
	case "remove-user":
		return runtime.Commands.RemoveUserFromDatabus(args.Name, args.Email)
	default:
		return fmt.Errorf("unsupported databus command: %s", args.Command)
	}
}

func (runtime *Runtime) executeObservability(args cli.Args) error {
	switch args.Command {
	case "create":
		return runtime.Commands.CreateObsService(args.Name, args.NetworkName)
	case "list":
		return runtime.Commands.ListObservabilityServices(args.Short)
	case "query":
		return runtime.Commands.QueryObservabilityService(args.Name)
	case "delete":
		return runtime.Commands.DeleteObservabilityService(args.Name)
	case "disable":
		return runtime.Commands.UpdateDatabusStatus(args.Name, "disable")
	case "resume":
		return runtime.Commands.UpdateDatabusStatus(args.Name, "resume")
	default:
		return fmt.Errorf("unsupported observability command: %s", args.Command)
	}
}

func (runtime *Runtime) executeClient(args cli.Args) error {
	switch args.Command {
	case "create":
		return runtime.Commands.CreateClientConfig(args.Name, args.Port, args.Kind, args.ClientName)
	case "get":
		return runtime.Commands.GetClientConfig(args.Name, args.ClientName, args.Example, args.Force, "")
	case "delete":
		return runtime.Commands.DeleteClientConfig(args.Name, args.ClientName)
	default:
		return fmt.Errorf("unsupported client command: %s", args.Command)
	}
}

func (runtime *Runtime) executeAppClient(args cli.Args) error {
	switch args.Command {
	case "list":
		return runtime.Commands.ListAppClients(args.Name, args.AppName)
	case "register":
		return runtime.Commands.RegisterAppClient(args.Name, args.AppName, args.ClientID, args.CSRFile, args.GenPrivateKey, args.Force)
	case "revoke":
		return runtime.Commands.RevokeAppClient(args.Name, args.AppName, args.ClientID)
	default:
		return fmt.Errorf("unsupported app-client command: %s", args.Command)
	}
}

func (runtime *Runtime) executeNetwork(args cli.Args) error {
	switch args.Command {
	case "list":
		return runtime.Commands.ListNetworks()
	case "delete":
		return runtime.Commands.DeleteNetwork(args.Name)
	default:
		return fmt.Errorf("unsupported network command: %s", args.Command)
	}
}

func (runtime *Runtime) executeLicense(args cli.Args) error {
	var expirationDays *int
	if args.HasExpirationDays {
		expirationDays = &args.ExpirationDays
	}
	return runtime.Commands.GetLicense(expirationDays, args.Output)
}

func (runtime *Runtime) executeGateway(args cli.Args) error {
	switch args.GatewayCommand {
	case "status":
		return runtime.Gateway.Status()
	case "reset":
		return runtime.Gateway.Reset()
	case "obs":
		return runtime.Gateway.OpenObservabilityDashboard()
	default:
		configValues, err := runtime.Gateway.ReadConfig()
		if err != nil {
			return err
		}
		if configValues == nil {
			configValues, err = runtime.Gateway.ConfigureFirstRun(!runtime.liveTextOutput(args))
			if err != nil {
				return err
			}
		}
		runtime.Gateway.PrintConfigSummary(configValues)
		if err := runtime.Gateway.ValidateConfigResources(configValues); err != nil {
			return err
		}
		if err := runtime.Gateway.DownloadArtifacts(configValues, false); err != nil {
			return err
		}
		databusSecure, collectorSecure, err := runtime.Gateway.EnsureSecureArtifacts(configValues)
		if err != nil {
			return err
		}
		var connext gateway.ConnextInstall
		if gateway.HasDatabus(configValues) {
			runtimeConfig, _ := configValues["runtime"].(map[string]any)
			connextHome, _ := runtimeConfig["connext_home"].(string)
			if connextHome != "" {
				connext, err = gateway.ValidateConnextInstall(connextHome)
			} else {
				connext, err = gateway.DiscoverConnextInstall(nil)
			}
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(runtime.Out, "Connext Pro %s found at %s\n", connext.Version, connext.Path)
		}
		collectorName := ""
		if gateway.HasObservability(configValues) {
			collectorName, err = runtime.Gateway.StartCollectorContainer(configValues, connext, collectorSecure)
			if err != nil {
				return err
			}
		}
		if gateway.HasDatabus(configValues) {
			_, err = runtime.Gateway.RunRoutingServiceWithOptions(configValues, connext, collectorName, databusSecure, collectorSecure, gateway.RunOptions{TextOutput: runtime.liveTextOutput(args)})
			return err
		}
		if err := runtime.Gateway.WriteRuntimeState(map[string]any{"routing_pid": nil, "started_at": runtime.Gateway.Now().UTC().Format("2006-01-02T15:04:05Z"), "collector_container": collectorName}); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(runtime.Out, "Gateway observability forwarding is running.")
		return nil
	}
}

func (runtime *Runtime) liveTextOutput(args cli.Args) bool {
	return args.Format == "text" || terminal.PlainOutputRequested(runtime.Out)
}
