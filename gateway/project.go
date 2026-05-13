package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"gopkg.in/yaml.v3"
)

const (
	CollectorImage             = "rticom/collector-service:7.7.0"
	CreateNewTemplate          = "__create_new_template__"
	CreateNewTemplateLabel     = "Create new template..."
	ReloadTemplateListLabel    = "Reload template list"
	CancelGatewaySetupLabel    = "Cancel gateway setup"
	RoutingLogName             = "routing.log"
	routingRenderPollInterval  = 50 * time.Millisecond
	routingLiveRefreshInterval = 250 * time.Millisecond
)

type TemplateItem = common.TemplateItem

type GatewayApp struct {
	WorkDir                  string
	In                       io.Reader
	Out                      io.Writer
	APIGet                   func(path string) (map[string]any, error)
	APIPost                  func(path string, payload map[string]any) (map[string]any, error)
	ListResourcesFunc        func() (map[string]map[string]any, map[string]map[string]any, error)
	GetResourceFunc          func(name string) (map[string]any, error)
	CurrentZoneFunc          func() string
	DiscoverConnextInstallFn func(prompt bool) (ConnextInstall, error)
	GenerateCSRFunc          func(databus string, app string, clientID string) ([]byte, string, error)
	CreateApplicationFunc    func(databusName string, kind string, clientName string) error
	DockerAvailableFunc      func() bool
	RunDockerFunc            func(args []string, check bool) (string, error)
	DownloadArtifactsFunc    func(config map[string]any, force bool) error
	CollectorStateFunc       func(name string) (string, string, error)
	PIDRunningFunc           func(pid int) bool
	SelectFunc               func(message string, choices []string) (string, error)
	InputFunc                func(message string) (string, error)
	ConfirmReloadFunc        func(message string) (bool, error)
	InterruptSignalFunc      func() (<-chan os.Signal, func())
	OpenBrowserFunc          func(url string) error
	Now                      func() time.Time
}

type RunOptions struct {
	TextOutput bool
}

func NewGatewayApp(workDir string, out io.Writer) *GatewayApp {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	app := &GatewayApp{
		WorkDir: workDir,
		In:      os.Stdin,
		Out:     out,
		Now:     time.Now,
		DockerAvailableFunc: func() bool {
			_, err := exec.LookPath("docker")
			return err == nil
		},
		RunDockerFunc: func(args []string, check bool) (string, error) {
			command := exec.Command("docker", args...)
			output, err := command.CombinedOutput()
			if err != nil && check {
				return string(output), err
			}
			return string(output), nil
		},
		OpenBrowserFunc: func(url string) error {
			var command *exec.Cmd
			switch runtime.GOOS {
			case "darwin":
				command = exec.Command("open", url)
			case "linux":
				command = exec.Command("xdg-open", url)
			case "windows":
				command = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
			default:
				return nil
			}
			return command.Start()
		},
	}
	app.SelectFunc = app.defaultSelect
	app.InputFunc = app.defaultInput
	app.ConfirmReloadFunc = app.defaultConfirmReload
	app.PIDRunningFunc = terminal.ProcessRunning
	return app
}

func (app *GatewayApp) ConfigPath() string {
	return filepath.Join(app.WorkDir, ".connext", "gateway.yaml")
}

func (app *GatewayApp) GatewayDir() string {
	return filepath.Join(app.WorkDir, ".connext", "gateway")
}

func (app *GatewayApp) RoutingDir() string {
	return filepath.Join(app.GatewayDir(), "routing")
}

func (app *GatewayApp) CollectorDir() string {
	return filepath.Join(app.GatewayDir(), "collector")
}

func (app *GatewayApp) RuntimePath() string {
	return filepath.Join(app.GatewayDir(), "runtime.json")
}

func (app *GatewayApp) LogsDir() string {
	return filepath.Join(app.GatewayDir(), "logs")
}

func (app *GatewayApp) RoutingLogPath() string {
	return filepath.Join(app.LogsDir(), RoutingLogName)
}

func (app *GatewayApp) ReadConfig() (map[string]any, error) {
	data, err := os.ReadFile(app.ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	config := map[string]any{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return config, nil
}

func (app *GatewayApp) WriteConfig(config map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(app.ConfigPath()), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(app.ConfigPath(), data, 0o644)
}

func (app *GatewayApp) RuntimeState() (map[string]any, error) {
	data, err := os.ReadFile(app.RuntimePath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	state := map[string]any{}
	if err := json.Unmarshal(data, &state); err != nil {
		return map[string]any{}, nil
	}
	return state, nil
}

func (app *GatewayApp) WriteRuntimeState(state map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(app.RuntimePath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(app.RuntimePath(), data, 0o644)
}

func ProjectID(config map[string]any) string {
	value := common.StringValue(config, "databus")
	if value == "" {
		value = common.StringValue(config, "observability")
	}
	return common.ProjectID(value, "project")
}

func HasDatabus(config map[string]any) bool {
	return common.StringValue(config, "databus") != "" && common.NestedString(config, "templates", "gateway") != ""
}

func HasObservability(config map[string]any) bool {
	return common.StringValue(config, "observability") != "" && common.NestedString(config, "templates", "collector") != ""
}

func TemplateItems(resource map[string]any, expectedKind string) []TemplateItem {
	return common.TemplateItems(resource, expectedKind)
}

func LinkedObservabilityName(databus map[string]any) string {
	config, _ := databus["config"].(map[string]any)
	linked := config["observability_service"]
	if linked == nil {
		linked = databus["observability_service"]
	}
	switch typed := linked.(type) {
	case map[string]any:
		value, _ := typed["name"].(string)
		return value
	case string:
		return typed
	default:
		return ""
	}
}

func (app *GatewayApp) DownloadArtifacts(config map[string]any, force bool) error {
	if app.DownloadArtifactsFunc != nil {
		return app.DownloadArtifactsFunc(config, force)
	}
	templates, _ := config["templates"].(map[string]any)
	if databus := common.StringValue(config, "databus"); databus != "" {
		if gatewayTemplate, _ := templates["gateway"].(string); gatewayTemplate != "" {
			target := filepath.Join(app.RoutingDir(), gatewayTemplate+".xml")
			if force || !common.FileExists(target) {
				if err := app.downloadTemplate(databus, gatewayTemplate, target); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(app.Out, "Downloaded gateway template: %s\n", target)
			}
		}
	}
	if observability := common.StringValue(config, "observability"); observability != "" {
		if collectorTemplate, _ := templates["collector"].(string); collectorTemplate != "" {
			target := filepath.Join(app.CollectorDir(), collectorTemplate+".xml")
			if force || !common.FileExists(target) {
				if err := app.downloadTemplate(observability, collectorTemplate, target); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(app.Out, "Downloaded collector template: %s\n", target)
			}
		}
	}
	return nil
}

func (app *GatewayApp) downloadTemplate(resourceName string, templateName string, target string) error {
	if app.APIGet == nil {
		return fmt.Errorf("API getter is not configured")
	}
	payload, err := app.APIGet(fmt.Sprintf("/databuses/%s/applications/%s", resourceName, templateName))
	if err != nil {
		return err
	}
	clientConfig := payload["client_config"]
	if clientConfig == nil {
		return GatewayError{Message: fmt.Sprintf("Error: Unexpected template response for '%s'", templateName)}
	}
	var content string
	switch typed := clientConfig.(type) {
	case string:
		content = typed
	default:
		encoded, _ := json.MarshalIndent(typed, "", "  ")
		content = string(encoded)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

func (app *GatewayApp) EnsureSecureArtifacts(config map[string]any) (bool, bool, error) {
	var databus map[string]any
	var observability map[string]any
	var err error
	if HasDatabus(config) {
		databus, err = app.getResource(common.StringValue(config, "databus"))
		if err != nil {
			return false, false, err
		}
	}
	if HasObservability(config) {
		observability, err = app.getResource(common.StringValue(config, "observability"))
		if err != nil {
			return false, false, err
		}
	}
	databusSecure := common.IsSecure(databus)
	collectorSecure := common.IsSecure(observability)
	clients, _ := config["clients"].(map[string]any)
	if databusSecure {
		_, _ = fmt.Fprintln(app.Out, "Secure Databus detected.")
		clientID, _ := clients["gateway_client_id"].(string)
		if clientID == "" {
			clientID = common.GenerateClientID()
		}
		if err := app.ensureSecureCredentials(common.StringValue(config, "databus"), common.NestedString(config, "templates", "gateway"), clientID, app.RoutingDir(), "gateway"); err != nil {
			return false, false, err
		}
	}
	if collectorSecure {
		_, _ = fmt.Fprintln(app.Out, "Secure Observability Service detected.")
		clientID, _ := clients["collector_client_id"].(string)
		if clientID == "" {
			clientID = common.GenerateClientID()
		}
		if err := app.ensureSecureCredentials(common.StringValue(config, "observability"), common.NestedString(config, "templates", "collector"), clientID, filepath.Join(app.CollectorDir(), "secure"), "collector"); err != nil {
			return false, false, err
		}
	}
	return databusSecure, collectorSecure, nil
}

func (app *GatewayApp) ensureSecureCredentials(resourceName string, templateName string, clientID string, targetDir string, label string) error {
	if common.LocalSecureFilesExist(targetDir) {
		_, _ = fmt.Fprintf(app.Out, "Reusing local %s credentials\n", label)
		_, _ = fmt.Fprintln(app.Out)
		return nil
	}
	if app.GenerateCSRFunc == nil || app.APIPost == nil {
		return fmt.Errorf("secure credential dependencies are not configured")
	}
	privateKey, csr, err := app.GenerateCSRFunc(resourceName, templateName, clientID)
	if err != nil {
		return err
	}
	payload, err := app.APIPost(fmt.Sprintf("/databuses/%s/applications/%s/clients", resourceName, templateName), map[string]any{"client_id": clientID, "csr": csr})
	if err != nil {
		return GatewayError{Message: fmt.Sprintf("Unable to register secure %s credentials for template '%s'.\n%v", label, templateName, err)}
	}
	securePayload, _ := payload["secure_files"].(map[string]any)
	secureMap := map[string]string{}
	for key, value := range securePayload {
		if text, ok := value.(string); ok {
			secureMap[key] = text
		}
	}
	if err := common.SaveSecureFiles(secureMap, privateKey, targetDir); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(app.Out, "Registered %s client credentials\n", label)
	_, _ = fmt.Fprintf(app.Out, "Saved %s credentials to %s\n", label, targetDir)
	_, _ = fmt.Fprintln(app.Out)
	return nil
}

func CollectorContainerName(config map[string]any) string {
	return fmt.Sprintf("rti-cloud-gateway-collector-%s", ProjectID(config))
}

func (app *GatewayApp) CollectorState(name string) (string, string, error) {
	if app.CollectorStateFunc != nil {
		return app.CollectorStateFunc(name)
	}
	if !app.DockerAvailableFunc() {
		return "unavailable", "", nil
	}
	output, err := app.RunDockerFunc([]string{"ps", "-a", "--filter", fmt.Sprintf("name=^%s$", name), "--format", "{{.Status}}"}, false)
	if err != nil {
		return "unavailable", "", nil
	}
	status := strings.TrimSpace(output)
	if status == "" {
		return "missing", "", nil
	}
	if strings.HasPrefix(strings.ToLower(status), "up") {
		return "running", "", nil
	}
	match := regexp.MustCompile(`exited \((\d+)\)`).FindStringSubmatch(strings.ToLower(status))
	if match != nil {
		return "exited", match[1], nil
	}
	return "exited", "", nil
}

func (app *GatewayApp) StartCollectorContainer(config map[string]any, connext ConnextInstall, secure bool) (string, error) {
	if !app.DockerAvailableFunc() {
		return "", GatewayError{Message: "Docker is required to run the Collector Service in this version.\n\nInstall/start Docker, then rerun:\n  rticloud gateway"}
	}
	name := CollectorContainerName(config)
	state, _, err := app.CollectorState(name)
	if err != nil {
		return "", err
	}
	if state == "running" {
		_, _ = fmt.Fprintf(app.Out, "Collector container already running: %s\n", name)
		return name, nil
	}
	if state == "exited" {
		if _, err := app.RunDockerFunc([]string{"rm", name}, true); err != nil {
			return "", err
		}
	}
	collectorTemplate := common.NestedString(config, "templates", "collector")
	if collectorTemplate == "" {
		return "", GatewayError{Message: "No Observability collector template is configured for this gateway."}
	}
	collectorXML := filepath.Join(app.CollectorDir(), collectorTemplate+".xml")
	licenseFile, err := app.licenseFile(connext)
	if err != nil {
		return "", err
	}
	args := []string{
		"run", "--platform", "linux/amd64", "-dt", "--network", "host",
		"-v", fmt.Sprintf("%s:/opt/rti.com/rti_connext_dds-7.7.0/rti_license.dat", licenseFile),
		"-v", fmt.Sprintf("%s:/opt/rti.com/EDGE_QOS/EDGE_COLLECTOR_SERVICE_QOS.xml", collectorXML),
		"-e", "RTI_LICENSE_FILE=/opt/rti.com/rti_connext_dds-7.7.0/rti_license.dat",
		"-e", "CFG_FILE=/opt/rti.com/EDGE_QOS/EDGE_COLLECTOR_SERVICE_QOS.xml",
		"-e", "CFG_NAME=" + collectorTemplate,
		"--name", name,
	}
	if secure {
		args = append(args, "-v", fmt.Sprintf("%s:/home/rtiuser/rti_workspace/7.7.0/user_config/collector_service/secure", filepath.Join(app.CollectorDir(), "secure")))
	}
	args = append(args, CollectorImage)
	if _, err := app.RunDockerFunc(args, true); err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(app.Out, "Collector container started: %s\n", name)
	return name, nil
}

func (app *GatewayApp) licenseFile(connext ConnextInstall) (string, error) {
	for _, envName := range []string{"RTI_LICENSE_FILE", "NDDS_LICENSE_FILE"} {
		if value := os.Getenv(envName); value != "" && common.FileExists(value) {
			return value, nil
		}
	}
	local := filepath.Join(app.CollectorDir(), "rti_license.dat")
	if common.FileExists(local) {
		return local, nil
	}
	if connext.Path != "" {
		candidate := filepath.Join(connext.Path, "rti_license.dat")
		if common.FileExists(candidate) {
			return candidate, nil
		}
	}
	return "", GatewayError{Message: "An RTI license is required to run the Collector Service.\n\nSet RTI_LICENSE_FILE to a valid license file, then rerun:\n  rticloud gateway"}
}

func (app *GatewayApp) RunRoutingService(config map[string]any, connext ConnextInstall, collectorName string, databusSecure bool, collectorSecure bool) (int, error) {
	return app.RunRoutingServiceWithOptions(config, connext, collectorName, databusSecure, collectorSecure, RunOptions{})
}

func (app *GatewayApp) RunRoutingServiceWithOptions(config map[string]any, connext ConnextInstall, collectorName string, databusSecure bool, collectorSecure bool, options RunOptions) (int, error) {
	gatewayTemplate := common.NestedString(config, "templates", "gateway")
	if gatewayTemplate == "" {
		return 0, GatewayError{Message: "No Databus gateway template is configured for this gateway."}
	}
	xmlPath := filepath.Join(app.RoutingDir(), gatewayTemplate+".xml")
	command := []string{
		RoutingExecutable(connext.Path),
		"-cfgFile", xmlPath,
		"-cfgName", gatewayTemplate + "_gateway",
		"-verbosity", "LOCAL:WARN",
	}
	if err := os.MkdirAll(app.LogsDir(), 0o755); err != nil {
		return 0, err
	}
	liveView := NewRoutingLiveView(config)
	liveView.CollectorName = collectorName
	liveView.DatabusSecure = databusSecure
	liveView.CollectorSecure = collectorSecure
	liveView.SeedFromConfig(xmlPath)
	renderer := TerminalRenderer{Out: app.Out}
	logFile, err := os.OpenFile(app.RoutingLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	cmd := exec.CommandContext(context.Background(), command[0], command[1:]...)
	cmd.Dir = app.RoutingDir()
	cmd.Env = mergeEnv(os.Environ(), app.routingEnv()...)
	stdout, stderr, err := startRoutingProcess(cmd)
	if err != nil {
		return 0, err
	}
	defer closeIfNotNil(stdout)
	defer closeIfNotNil(stderr)
	if err := app.WriteRuntimeState(map[string]any{
		"routing_pid":         cmd.Process.Pid,
		"started_at":          app.Now().UTC().Format(time.RFC3339),
		"collector_container": collectorName,
	}); err != nil {
		return 0, err
	}
	routingLines := make(chan string, 128)
	handleRoutingLine := func(line string) {
		if strings.TrimSpace(line) == "" {
			return
		}
		_, _ = fmt.Fprintf(logFile, "%s [routing] %s\n", app.Now().UTC().Format(time.RFC3339), line)
		liveView.HandleLine(line)
		if options.TextOutput {
			for _, eventLine := range PlainEventLines(line) {
				_, _ = fmt.Fprintln(app.Out, eventLine)
			}
		}
	}
	var streamWG sync.WaitGroup
	stream := func(reader io.Reader) {
		defer streamWG.Done()
		if reader == nil {
			return
		}
		buffer := make([]byte, 4096)
		pending := ""
		flushPending := func(force bool) {
			pending = strings.ReplaceAll(pending, "\r\n", "\n")
			pending = strings.ReplaceAll(pending, "\r", "\n")
			for {
				index := strings.IndexByte(pending, '\n')
				if index < 0 {
					break
				}
				line := pending[:index]
				pending = pending[index+1:]
				routingLines <- line
			}
			if force && pending != "" {
				routingLines <- pending
				pending = ""
			}
		}
		for {
			count, readErr := reader.Read(buffer)
			if count > 0 {
				pending += string(buffer[:count])
				flushPending(false)
			}
			if readErr != nil {
				flushPending(true)
				return
			}
		}
	}
	streamWG.Add(1)
	go stream(stdout)
	if stderr != nil {
		streamWG.Add(1)
		go stream(stderr)
	}
	go func() {
		streamWG.Wait()
		close(routingLines)
	}()
	interrupts, stopInterrupts := app.interruptSignals()
	defer stopInterrupts()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()
	if !options.TextOutput {
		_ = renderer.Render(liveView.Render(liveView.PulseFrame()))
	}
	renderTicker := time.NewTicker(routingRenderPollInterval)
	defer renderTicker.Stop()
	var killTimer *time.Timer
	interrupted := false
	processExited := false
	lastLiveRefresh := time.Time{}
	lastPulseFrame := liveView.PulseFrame()
	pendingLiveRefresh := false
	renderCurrentView := func(now time.Time) {
		currentPulseFrame := liveView.PulseFrame(float64(now.UnixNano()) / float64(time.Second))
		lastPulseFrame = currentPulseFrame
		if !options.TextOutput {
			_ = renderer.Render(liveView.Render(currentPulseFrame))
		}
		lastLiveRefresh = now
		pendingLiveRefresh = false
	}
	for {
		select {
		case err = <-waitDone:
			waitDone = nil
			processExited = true
			if killTimer != nil {
				killTimer.Stop()
			}
			if routingLines == nil {
				goto done
			}
		case line, ok := <-routingLines:
			if !ok {
				routingLines = nil
				if processExited {
					goto done
				}
				continue
			}
			handleRoutingLine(line)
			for {
				select {
				case line, ok = <-routingLines:
					if !ok {
						routingLines = nil
						if processExited {
							goto done
						}
						goto drainedRoutingLines
					}
					handleRoutingLine(line)
				default:
					goto drainedRoutingLines
				}
			}
		drainedRoutingLines:
			pendingLiveRefresh = true
			now := app.Now()
			if lastLiveRefresh.IsZero() || now.Sub(lastLiveRefresh) >= routingRenderPollInterval {
				renderCurrentView(now)
			}
		case <-renderTicker.C:
			now := app.Now()
			pulseChanged := false
			if liveView.HasActivePulse() {
				currentPulseFrame := liveView.PulseFrame(float64(now.UnixNano()) / float64(time.Second))
				pulseChanged = currentPulseFrame != lastPulseFrame
				lastPulseFrame = currentPulseFrame
			}
			if pulseChanged {
				renderCurrentView(now)
			} else if pendingLiveRefresh && (lastLiveRefresh.IsZero() || now.Sub(lastLiveRefresh) >= routingLiveRefreshInterval) {
				renderCurrentView(now)
			}
		case <-interrupts:
			if interrupted {
				continue
			}
			interrupted = true
			liveView.State.serviceState = "stopping"
			if cmd.Process != nil {
				terminal.SendInterrupt(cmd.Process)
				killTimer = time.AfterFunc(2*time.Second, func() {
					_ = cmd.Process.Kill()
				})
			}
		}
	}

done:
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if !options.TextOutput {
				_ = renderer.Render(liveView.PrintSnapshot("stopped"))
			}
			if interrupted {
				_, _ = fmt.Fprintf(app.Out, "Gateway interrupted.\n")
			} else {
				_, _ = fmt.Fprintf(app.Out, "Gateway stopped.\n")
			}
			app.printGatewayRestartHint()
			if interrupted {
				return 130, nil
			}
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
	if !options.TextOutput {
		_ = renderer.Render(liveView.PrintSnapshot("stopped"))
	}
	if interrupted {
		_, _ = fmt.Fprintf(app.Out, "Gateway interrupted.\n")
	}
	app.printGatewayRestartHint()
	return 0, nil
}

func (app *GatewayApp) routingEnv() []string {
	env := []string{"RTI_MONITORING2_ENABLE=false"}
	privateKeyPath := filepath.Join(app.RoutingDir(), "client.key")
	if common.FileExists(privateKeyPath) {
		env = append(env, "RTI_PRIVATE_KEY_FILE="+privateKeyPath)
	}
	return env
}

func mergeEnv(base []string, overrides ...string) []string {
	merged := append([]string{}, base...)
	for _, override := range overrides {
		key, _, found := strings.Cut(override, "=")
		if !found {
			merged = append(merged, override)
			continue
		}
		replaced := false
		for index, value := range merged {
			existingKey, _, existingFound := strings.Cut(value, "=")
			if existingFound && existingKey == key {
				merged[index] = override
				replaced = true
				break
			}
		}
		if !replaced {
			merged = append(merged, override)
		}
	}
	return merged
}

func (app *GatewayApp) printGatewayRestartHint() {
	_, _ = fmt.Fprintln(app.Out, "• Run 'rticloud gateway' from this directory to start this gateway again.")
}

func supportsRoutingPTY() bool {
	return terminal.SupportsPTY()
}

func startRoutingProcess(cmd *exec.Cmd) (io.ReadCloser, io.ReadCloser, error) {
	return terminal.StartProcess(cmd)
}

func closeIfNotNil(closer io.Closer) {
	if closer != nil {
		_ = closer.Close()
	}
}

func (app *GatewayApp) interruptSignals() (<-chan os.Signal, func()) {
	if app.InterruptSignalFunc != nil {
		return app.InterruptSignalFunc()
	}
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, terminal.InterruptSignals()...)
	return interrupts, func() {
		signal.Stop(interrupts)
		close(interrupts)
	}
}

func (app *GatewayApp) Status() error {
	config, err := app.ReadConfig()
	if err != nil {
		return err
	}
	if config == nil {
		_, _ = fmt.Fprintln(app.Out, "No gateway configuration found in this project.")
		_, _ = fmt.Fprintln(app.Out, "Run:")
		_, _ = fmt.Fprintln(app.Out, "  rticloud gateway")
		return nil
	}
	runtimeState, err := app.RuntimeState()
	if err != nil {
		return err
	}
	app.PrintConfigSummary(config)
	pid := intFromAny(runtimeState["routing_pid"])
	collectorName, _ := runtimeState["collector_container"].(string)
	if collectorName == "" {
		collectorName = CollectorContainerName(config)
	}
	routing := "stopped"
	if !HasDatabus(config) {
		routing = "not configured"
	} else if pid > 0 && app.pidRunning(pid) {
		routing = fmt.Sprintf("running (pid %d)", pid)
	} else if pid > 0 {
		routing = fmt.Sprintf("stopped (stale pid %d)", pid)
	}
	collector := "not configured"
	if HasObservability(config) {
		state, code, _ := app.CollectorState(collectorName)
		switch state {
		case "running":
			collector = fmt.Sprintf("running (%s)", collectorName)
		case "exited":
			if code == "" {
				code = "unknown"
			}
			collector = fmt.Sprintf("exited (code %s)", code)
		case "unavailable":
			collector = "unknown (Docker unavailable)"
		default:
			collector = "stopped"
		}
	}
	_, _ = fmt.Fprintf(app.Out, "Routing Service: %s\n", routing)
	_, _ = fmt.Fprintf(app.Out, "Collector: %s\n", collector)
	connextHome := common.NestedString(config, "runtime", "connext_home")
	if connextHome != "" && HasDatabus(config) {
		connext, err := ValidateConnextInstall(connextHome)
		if err != nil {
			_, _ = fmt.Fprintf(app.Out, "Connext: unavailable (%s)\n", connextHome)
		} else {
			_, _ = fmt.Fprintf(app.Out, "Connext: %s (%s)\n", connext.Version, connext.Path)
		}
	}
	return nil
}

func (app *GatewayApp) PrintConfigSummary(config map[string]any) {
	_, _ = fmt.Fprintln(app.Out, "Gateway configuration:")
	_, _ = fmt.Fprintf(app.Out, "  Databus: %s\n", fallbackString(common.StringValue(config, "databus"), "not configured"))
	_, _ = fmt.Fprintf(app.Out, "  Observability: %s\n", fallbackString(common.StringValue(config, "observability"), "not configured"))
	_, _ = fmt.Fprintf(app.Out, "  Gateway template: %s\n", fallbackString(common.NestedString(config, "templates", "gateway"), "not configured"))
	_, _ = fmt.Fprintf(app.Out, "  Collector: %s\n", fallbackString(common.NestedString(config, "templates", "collector"), "not configured"))
	_, _ = fmt.Fprintln(app.Out)
}

func (app *GatewayApp) Reset() error {
	if !common.FileExists(app.ConfigPath()) {
		_, _ = fmt.Fprintln(app.Out, "No gateway configuration found.")
		return nil
	}
	config, _ := app.ReadConfig()
	if err := os.Remove(app.ConfigPath()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(app.Out, "Removed %s\n", app.ConfigPath())
	_, _ = fmt.Fprintln(app.Out)
	_, _ = fmt.Fprintf(app.Out, "Runtime artifacts were left in\nPath: %s\n", app.GatewayDir())
	_, _ = fmt.Fprintf(app.Out, "Logs were left in\nPath: %s\n", app.LogsDir())
	_ = config
	return nil
}

func (app *GatewayApp) OpenObservabilityDashboard() error {
	config, err := app.ReadConfig()
	if err != nil {
		return err
	}
	if config == nil || common.StringValue(config, "observability") == "" {
		_, _ = fmt.Fprintln(app.Out, "No Observability Service is configured for this gateway.")
		return nil
	}
	resource, err := app.getResource(common.StringValue(config, "observability"))
	if err != nil {
		return err
	}
	url := grafanaURL(resource)
	if url == "" {
		_, _ = fmt.Fprintf(app.Out, "Unable to resolve Grafana URL for Observability Service '%s'.\n", common.StringValue(config, "observability"))
		return nil
	}
	_, _ = fmt.Fprintf(app.Out, "Opening Observability dashboard\nURL: %s\n", url)
	if app.OpenBrowserFunc != nil {
		return app.OpenBrowserFunc(url)
	}
	return nil
}

func (app *GatewayApp) ConfigureFirstRun(prompt bool) (map[string]any, error) {
	databuses, observabilityServices, err := app.listResources()
	if err != nil {
		return nil, err
	}
	if len(databuses) == 0 && len(observabilityServices) == 0 {
		return nil, GatewayError{Message: "No Databuses or Observability Services found."}
	}
	_, _, cursorSelection := app.promptTerminal()
	_, _ = fmt.Fprint(app.Out, RenderSetupIntro(len(databuses), len(observabilityServices), cursorSelection))
	connext := ConnextInstall{}
	if len(databuses) > 0 {
		connext, err = app.discoverConnextInstall(prompt)
		if err != nil {
			return nil, err
		}
		connextMsg := fmt.Sprintf("Using Connext Pro %s at %s", connext.Version, connext.Path)
		if connext.Reason != "" {
			connextMsg += fmt.Sprintf(" (%s)", connext.Reason)
		}
		_, _ = fmt.Fprint(app.Out, RenderInfoMessage(connextMsg))
	}
	capabilityChoices := []string{}
	if len(databuses) > 0 && len(observabilityServices) > 0 {
		capabilityChoices = append(capabilityChoices, "Data and Observability")
	}
	if len(databuses) > 0 {
		capabilityChoices = append(capabilityChoices, "Data only")
	}
	if len(observabilityServices) > 0 {
		capabilityChoices = append(capabilityChoices, "Observability only")
	}
	capability, err := app.choose("Select gateway capability:", capabilityChoices)
	if err != nil {
		return nil, err
	}
	includeData := capability == "Data and Observability" || capability == "Data only"
	includeObservability := capability == "Data and Observability" || capability == "Observability only"
	if !includeData {
		connext = ConnextInstall{}
	}
	databusName := ""
	gatewayTemplate := ""
	var databus map[string]any
	if includeData {
		databusName, err = app.choose("Select Databus:", common.SortedKeys(databuses))
		if err != nil {
			return nil, err
		}
		databus, err = app.getResource(databusName)
		if err != nil {
			return nil, err
		}
		gatewayTemplates := TemplateItems(databus, "gateway")
		gatewayTemplate, err = app.selectTemplateOrCreate(databusName, "Databus", "gateway", fmt.Sprintf("Select Gateway template from %s:", databusName), gatewayTemplates)
		if err != nil {
			return nil, err
		}
	}
	observabilityName := ""
	collectorTemplate := ""
	if includeObservability {
		linkedObs := ""
		if databus != nil {
			linkedObs = LinkedObservabilityName(databus)
		}
		obsNames := common.SortedKeys(observabilityServices)
		obsChoices := make([]string, 0, len(obsNames))
		if linkedObs != "" && contains(obsNames, linkedObs) {
			obsChoices = append(obsChoices, choiceWithLabel(linkedObs, fmt.Sprintf("%s  (linked to %s)", linkedObs, databusName)))
			obsNames = remove(obsNames, linkedObs)
		}
		for _, name := range obsNames {
			obsChoices = append(obsChoices, name)
		}
		observabilityName, err = app.choose("Select Observability Service:", obsChoices)
		if err != nil {
			return nil, err
		}
		observability, err := app.getResource(observabilityName)
		if err != nil {
			return nil, err
		}
		collectorTemplates := TemplateItems(observability, "observability-collector")
		if includeData {
			autoName := sanitizeCollectorName(databusName + "_" + gatewayTemplate)
			if common.TemplateListContains(collectorTemplates, autoName) {
				collectorTemplate = autoName
				_, _ = fmt.Fprint(app.Out, RenderInfoMessage(fmt.Sprintf("Using collector template: %s", autoName)))
			} else {
				_, _ = fmt.Fprint(app.Out, RenderInfoMessage(fmt.Sprintf("Creating collector template: %s", autoName)))
				collectorTemplate, err = app.createCollectorTemplate(observabilityName, autoName)
				if err != nil {
					return nil, err
				}
			}
		} else {
			collectorTemplate, err = app.selectCollectorOrCreate(observabilityName, fmt.Sprintf("Select Collector template from %s:", observabilityName), collectorTemplates)
			if err != nil {
				return nil, err
			}
		}
	}
	config := map[string]any{
		"zone":          app.currentZone(),
		"databus":       nullableString(databusName),
		"observability": nullableString(observabilityName),
		"templates": map[string]any{
			"gateway":   nullableString(gatewayTemplate),
			"collector": nullableString(collectorTemplate),
		},
		"runtime": map[string]any{"min_version": MinConnextVersion},
		"clients": map[string]any{
			"gateway_client_id":   nullableClientID(gatewayTemplate),
			"collector_client_id": nullableClientID(collectorTemplate),
		},
	}
	if includeData {
		config["runtime"].(map[string]any)["connext_home"] = connext.Path
	}
	if err := app.WriteConfig(config); err != nil {
		return nil, err
	}
	_, _ = fmt.Fprint(app.Out, RenderSuccessMessage(fmt.Sprintf("Configuration saved to %s", app.ConfigPath())))
	if err := app.DownloadArtifacts(config, true); err != nil {
		return nil, err
	}
	return config, nil
}

func (app *GatewayApp) ValidateConfigResources(config map[string]any) error {
	if !HasDatabus(config) && !HasObservability(config) {
		return GatewayError{Message: "No Databus or Observability Service is configured for this gateway."}
	}
	if HasDatabus(config) {
		databus, err := app.getResource(common.StringValue(config, "databus"))
		if err != nil {
			return err
		}
		gatewayTemplate := common.NestedString(config, "templates", "gateway")
		if !common.TemplateListContains(TemplateItems(databus, "gateway"), gatewayTemplate) {
			zone := common.StringValue(config, "zone")
			if zone == "" {
				zone = app.currentZone()
			}
			return GatewayError{Message: fmt.Sprintf("Gateway template '%s' was not found for Databus '%s'.\n\nCreate one from the Connext Cloud dashboard\n  - Open %s\nThen rerun:\n  rticloud gateway", gatewayTemplate, common.StringValue(config, "databus"), DashboardURL(zone, common.StringValue(config, "databus"), "databus"))}
		}
	}
	if HasObservability(config) {
		observability, err := app.getResource(common.StringValue(config, "observability"))
		if err != nil {
			return err
		}
		collectorTemplate := common.NestedString(config, "templates", "collector")
		if !common.TemplateListContains(TemplateItems(observability, "observability-collector"), collectorTemplate) {
			zone := common.StringValue(config, "zone")
			if zone == "" {
				zone = app.currentZone()
			}
			return GatewayError{Message: fmt.Sprintf("Collector template '%s' was not found for Observability Service '%s'.\n\nCreate one from the Connext Cloud dashboard\n  - Open %s\nThen rerun:\n  rticloud gateway", collectorTemplate, common.StringValue(config, "observability"), DashboardURL(zone, common.StringValue(config, "observability"), "observability"))}
		}
	}
	return nil
}

func (app *GatewayApp) loadTemplateItemsWithReload(resourceName string, resourceLabel string, templateKind string, missingTitle string) (map[string]any, []TemplateItem, error) {
	resource, err := app.getResource(resourceName)
	if err != nil {
		return nil, nil, err
	}
	templates := TemplateItems(resource, templateKind)
	if len(templates) > 0 {
		return resource, templates, nil
	}
	return app.waitForTemplateCreation(resourceName, resourceLabel, templateKind)
}

func (app *GatewayApp) waitForTemplateCreation(resourceName string, resourceLabel string, templateKind string) (map[string]any, []TemplateItem, error) {
	zone := app.currentZone()
	dashboard := DashboardURL(zone, resourceName, DashboardResourceKind(resourceLabel))
	for {
		title := "• Create template in Connext Cloud dashboard:"
		switch templateKind {
		case "gateway":
			title = "• Create gateway template in Connext Cloud dashboard:"
		case "telemetry-service-collector", "observability-collector":
			title = "• Create collector template in Connext Cloud dashboard:"
		}
		_, _ = fmt.Fprint(app.Out, RenderKeyValuePanel(title, []KeyValueRow{{Value: dashboard}}))
		confirm, err := app.confirmReload("Reload template list after creating it in the dashboard.")
		if err != nil {
			return nil, nil, err
		}
		if !confirm {
			return nil, nil, GatewayError{Message: "Gateway configuration cancelled."}
		}
		_, _ = fmt.Fprint(app.Out, RenderInfoMessage("Reloading templates..."))
		resource, err := app.getResource(resourceName)
		if err != nil {
			return nil, nil, err
		}
		templates := TemplateItems(resource, templateKind)
		if len(templates) > 0 {
			return resource, templates, nil
		}
	}
}

// sanitizeCollectorName replaces any character not in [a-zA-Z0-9_] with "_".
func sanitizeCollectorName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, name)
}

// createCollectorTemplate creates a new observability-collector application
// template in the given observability service.
func (app *GatewayApp) createCollectorTemplate(obsName, collectorName string) (string, error) {
	if app.CreateApplicationFunc != nil {
		return collectorName, app.CreateApplicationFunc(obsName, "telemetry-service-collector", collectorName)
	}
	if app.APIPost == nil {
		return "", fmt.Errorf("API not configured")
	}
	_, err := app.APIPost(fmt.Sprintf("/databuses/%s/applications", obsName), map[string]any{
		"kind":        "telemetry-service-collector",
		"client_name": collectorName,
	})
	if err != nil {
		return "", err
	}
	return collectorName, nil
}

// selectCollectorOrCreate shows existing collector templates and a
// "Create a new one..." option. If chosen, prompts for a name and
// creates the template via the API.
func (app *GatewayApp) selectCollectorOrCreate(obsName, selectMsg string, templates []TemplateItem) (string, error) {
	const createNewCollector = "Create a new one..."
	choices := make([]string, 0, len(templates)+1)
	for _, item := range templates {
		choices = append(choices, item.Name)
	}
	choices = append(choices, createNewCollector)
	selected, err := app.choose(selectMsg, choices)
	if err != nil {
		return "", err
	}
	if selected != createNewCollector {
		return selected, nil
	}
	name, err := app.InputFunc("Collector name:")
	if err != nil {
		return "", err
	}
	name = sanitizeCollectorName(strings.TrimSpace(name))
	if name == "" {
		return "", GatewayError{Message: "Collector name cannot be empty."}
	}
	return app.createCollectorTemplate(obsName, name)
}

func (app *GatewayApp) selectTemplateOrCreate(resourceName string, resourceLabel string, templateKind string, selectMessage string, templates []TemplateItem) (string, error) {
	for {
		choices := make([]string, 0, len(templates)+1)
		for _, item := range templates {
			choices = append(choices, item.Name)
		}
		choices = append(choices, CreateNewTemplate)
		selected, err := app.choose(selectMessage, choices)
		if err != nil {
			return "", err
		}
		if selected != CreateNewTemplate {
			return selected, nil
		}
		_, templates, err = app.waitForTemplateCreation(resourceName, resourceLabel, templateKind)
		if err != nil {
			return "", err
		}
	}
}

func (app *GatewayApp) getResource(name string) (map[string]any, error) {
	if app.GetResourceFunc != nil {
		return app.GetResourceFunc(name)
	}
	if app.APIGet == nil {
		return nil, fmt.Errorf("resource getter is not configured")
	}
	payload, err := app.APIGet("/databuses/" + name)
	if err != nil {
		return nil, err
	}
	payload["name"] = name
	return payload, nil
}

func (app *GatewayApp) listResources() (map[string]map[string]any, map[string]map[string]any, error) {
	if app.ListResourcesFunc != nil {
		return app.ListResourcesFunc()
	}
	if app.APIGet == nil {
		return nil, nil, fmt.Errorf("resource lister is not configured")
	}
	payload, err := app.APIGet("/databuses?extra_fields=true")
	if err != nil {
		return nil, nil, err
	}
	rawResources, _ := payload["databuses"].(map[string]any)
	databuses := map[string]map[string]any{}
	observability := map[string]map[string]any{}
	for name, rawInfo := range rawResources {
		info, _ := rawInfo.(map[string]any)
		kind, _ := info["kind"].(string)
		if kind == "telemetry" {
			observability[name] = info
		} else {
			databuses[name] = info
		}
	}
	return databuses, observability, nil
}

func (app *GatewayApp) currentZone() string {
	if app.CurrentZoneFunc != nil {
		return app.CurrentZoneFunc()
	}
	return "unknown"
}

func (app *GatewayApp) discoverConnextInstall(prompt bool) (ConnextInstall, error) {
	if app.DiscoverConnextInstallFn == nil {
		return ConnextInstall{}, fmt.Errorf("Connext discovery is not configured")
	}
	return app.DiscoverConnextInstallFn(prompt)
}

func (app *GatewayApp) choose(message string, choices []string) (string, error) {
	if app.SelectFunc == nil {
		return "", fmt.Errorf("interactive selection is not configured")
	}
	return app.SelectFunc(message, choices)
}

func (app *GatewayApp) confirmReload(message string) (bool, error) {
	if app.ConfirmReloadFunc == nil {
		return false, fmt.Errorf("interactive confirmation is not configured")
	}
	return app.ConfirmReloadFunc(message)
}

func (app *GatewayApp) defaultSelect(message string, choices []string) (string, error) {
	return app.selector().Select(message, choices)
}

func (app *GatewayApp) defaultInput(message string) (string, error) {
	return prompt.Input{In: app.input(), Out: app.Out, CancelMessage: "Gateway configuration cancelled."}.Prompt(message)
}

func selectionLabel(choice string) string {
	return prompt.Selector{SpecialLabels: map[string]string{CreateNewTemplate: CreateNewTemplateLabel}}.SelectionLabel(choice)
}

func selectionValue(choice string) string {
	return prompt.SelectionValue(choice)
}

func choiceWithLabel(value string, label string) string {
	return prompt.ChoiceWithLabel(value, label)
}

func splitChoice(choice string) (string, string, bool) {
	return prompt.SplitChoice(choice)
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func (app *GatewayApp) defaultConfirmReload(message string) (bool, error) {
	selection, err := app.defaultSelect(message, []string{ReloadTemplateListLabel, CancelGatewaySetupLabel})
	if err != nil {
		return false, err
	}
	return selection == ReloadTemplateListLabel, nil
}

func (app *GatewayApp) input() io.Reader {
	if app.In != nil {
		return app.In
	}
	return os.Stdin
}

func (app *GatewayApp) selector() prompt.Selector {
	return prompt.Selector{
		In:            app.input(),
		Out:           app.Out,
		CancelMessage: "Gateway configuration cancelled.",
		SpecialLabels: map[string]string{CreateNewTemplate: CreateNewTemplateLabel},
	}
}

func (app *GatewayApp) promptTerminal() (*os.File, *os.File, bool) {
	return terminal.PromptFiles(app.input(), app.Out)
}

func (app *GatewayApp) pidRunning(pid int) bool {
	if app.PIDRunningFunc != nil {
		return app.PIDRunningFunc(pid)
	}
	return terminal.ProcessRunning(pid)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableClientID(template string) any {
	if template == "" {
		return nil
	}
	return common.GenerateClientID()
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func remove(values []string, target string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func fallbackString(value string, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func grafanaURL(resource map[string]any) string {
	entrypoints, _ := resource["entrypoints"].(map[string]any)
	grafana := entrypoints["grafana"]
	switch typed := grafana.(type) {
	case map[string]any:
		value, _ := typed["url"].(string)
		return value
	case string:
		return typed
	default:
		return ""
	}
}
