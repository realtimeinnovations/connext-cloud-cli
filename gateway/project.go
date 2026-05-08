package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
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

var secureFiles = []string{
	"client.key",
	"client.crt",
	"identity_ca.crt",
	"permissions_ca.crt",
	"signed_governance.p7s",
	"signed_permissions.p7s",
	"psk.key",
}

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
			switch {
			case hasCommand("open"):
				command = exec.Command("open", url)
			case hasCommand("xdg-open"):
				command = exec.Command("xdg-open", url)
			default:
				return nil
			}
			return command.Start()
		},
	}
	app.SelectFunc = app.defaultSelect
	app.InputFunc = app.defaultInput
	app.ConfirmReloadFunc = app.defaultConfirmReload
	app.PIDRunningFunc = pidRunning
	return app
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
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
	value := stringValue(config, "databus")
	if value == "" {
		value = stringValue(config, "observability")
	}
	return common.ProjectID(value, "project")
}

func HasDatabus(config map[string]any) bool {
	return stringValue(config, "databus") != "" && nestedString(config, "templates", "gateway") != ""
}

func HasObservability(config map[string]any) bool {
	return stringValue(config, "observability") != "" && nestedString(config, "templates", "collector") != ""
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
	if databus := stringValue(config, "databus"); databus != "" {
		if gatewayTemplate, _ := templates["gateway"].(string); gatewayTemplate != "" {
			target := filepath.Join(app.RoutingDir(), gatewayTemplate+".xml")
			if force || !fileExists(target) {
				if err := app.downloadTemplate(databus, gatewayTemplate, target); err != nil {
					return err
				}
				_, _ = fmt.Fprintf(app.Out, "Downloaded gateway template: %s\n", target)
			}
		}
	}
	if observability := stringValue(config, "observability"); observability != "" {
		if collectorTemplate, _ := templates["collector"].(string); collectorTemplate != "" {
			target := filepath.Join(app.CollectorDir(), collectorTemplate+".xml")
			if force || !fileExists(target) {
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
		databus, err = app.getResource(stringValue(config, "databus"))
		if err != nil {
			return false, false, err
		}
	}
	if HasObservability(config) {
		observability, err = app.getResource(stringValue(config, "observability"))
		if err != nil {
			return false, false, err
		}
	}
	databusSecure := isSecure(databus)
	collectorSecure := isSecure(observability)
	clients, _ := config["clients"].(map[string]any)
	if databusSecure {
		_, _ = fmt.Fprintln(app.Out, "Secure Databus detected.")
		clientID, _ := clients["gateway_client_id"].(string)
		if clientID == "" {
			clientID = nestedString(config, "templates", "gateway") + "-1"
		}
		if err := app.ensureSecureCredentials(stringValue(config, "databus"), nestedString(config, "templates", "gateway"), clientID, app.RoutingDir(), "gateway"); err != nil {
			return false, false, err
		}
	}
	if collectorSecure {
		_, _ = fmt.Fprintln(app.Out, "Secure Observability Service detected.")
		clientID, _ := clients["collector_client_id"].(string)
		if clientID == "" {
			clientID = nestedString(config, "templates", "collector") + "-1"
		}
		if err := app.ensureSecureCredentials(stringValue(config, "observability"), nestedString(config, "templates", "collector"), clientID, filepath.Join(app.CollectorDir(), "secure"), "collector"); err != nil {
			return false, false, err
		}
	}
	return databusSecure, collectorSecure, nil
}

func (app *GatewayApp) ensureSecureCredentials(resourceName string, templateName string, clientID string, targetDir string, label string) error {
	if localSecureFilesExist(targetDir) {
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
	if err := saveSecureFiles(secureMap, privateKey, targetDir); err != nil {
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
	collectorTemplate := nestedString(config, "templates", "collector")
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
		if value := os.Getenv(envName); value != "" && fileExists(value) {
			return value, nil
		}
	}
	local := filepath.Join(app.CollectorDir(), "rti_license.dat")
	if fileExists(local) {
		return local, nil
	}
	if connext.Path != "" {
		candidate := filepath.Join(connext.Path, "rti_license.dat")
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	return "", GatewayError{Message: "An RTI license is required to run the Collector Service.\n\nSet RTI_LICENSE_FILE to a valid license file, then rerun:\n  rticloud gateway"}
}

func (app *GatewayApp) RunRoutingService(config map[string]any, connext ConnextInstall, collectorName string, databusSecure bool, collectorSecure bool) (int, error) {
	return app.RunRoutingServiceWithOptions(config, connext, collectorName, databusSecure, collectorSecure, RunOptions{})
}

func (app *GatewayApp) RunRoutingServiceWithOptions(config map[string]any, connext ConnextInstall, collectorName string, databusSecure bool, collectorSecure bool, options RunOptions) (int, error) {
	gatewayTemplate := nestedString(config, "templates", "gateway")
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
	stream := func(reader io.Reader) {
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
	go stream(stdout)
	if stderr != nil {
		go stream(stderr)
	}
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
			if killTimer != nil {
				killTimer.Stop()
			}
			goto done
		case line := <-routingLines:
			handleRoutingLine(line)
			for {
				select {
				case line = <-routingLines:
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
				_ = cmd.Process.Signal(os.Interrupt)
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
	if fileExists(privateKeyPath) {
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
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
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
	connextHome := nestedString(config, "runtime", "connext_home")
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
	_, _ = fmt.Fprintf(app.Out, "  Databus: %s\n", fallbackString(stringValue(config, "databus"), "not configured"))
	_, _ = fmt.Fprintf(app.Out, "  Observability: %s\n", fallbackString(stringValue(config, "observability"), "not configured"))
	_, _ = fmt.Fprintf(app.Out, "  Gateway template: %s\n", fallbackString(nestedString(config, "templates", "gateway"), "not configured"))
	_, _ = fmt.Fprintf(app.Out, "  Collector: %s\n", fallbackString(nestedString(config, "templates", "collector"), "not configured"))
	_, _ = fmt.Fprintln(app.Out)
}

func (app *GatewayApp) Reset() error {
	if !fileExists(app.ConfigPath()) {
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
	if config == nil || stringValue(config, "observability") == "" {
		_, _ = fmt.Fprintln(app.Out, "No Observability Service is configured for this gateway.")
		return nil
	}
	resource, err := app.getResource(stringValue(config, "observability"))
	if err != nil {
		return err
	}
	url := grafanaURL(resource)
	if url == "" {
		_, _ = fmt.Fprintf(app.Out, "Unable to resolve Grafana URL for Observability Service '%s'.\n", stringValue(config, "observability"))
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
		databusName, err = app.choose("Select Databus:", sortedKeys(databuses))
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
		obsNames := sortedKeys(observabilityServices)
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
		collectorTemplate, err = app.selectTemplateOrCreate(observabilityName, "Observability Service", "observability-collector", fmt.Sprintf("Select Collector template from %s:", observabilityName), collectorTemplates)
		if err != nil {
			return nil, err
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
		databus, err := app.getResource(stringValue(config, "databus"))
		if err != nil {
			return err
		}
		gatewayTemplate := nestedString(config, "templates", "gateway")
		if !templateListContains(TemplateItems(databus, "gateway"), gatewayTemplate) {
			zone := stringValue(config, "zone")
			if zone == "" {
				zone = app.currentZone()
			}
			return GatewayError{Message: fmt.Sprintf("Gateway template '%s' was not found for Databus '%s'.\n\nCreate one from the Connext Cloud dashboard\n  - Open %s\nThen rerun:\n  rticloud gateway", gatewayTemplate, stringValue(config, "databus"), DashboardURL(zone, stringValue(config, "databus"), "databus"))}
		}
	}
	if HasObservability(config) {
		observability, err := app.getResource(stringValue(config, "observability"))
		if err != nil {
			return err
		}
		collectorTemplate := nestedString(config, "templates", "collector")
		if !templateListContains(TemplateItems(observability, "observability-collector"), collectorTemplate) {
			zone := stringValue(config, "zone")
			if zone == "" {
				zone = app.currentZone()
			}
			return GatewayError{Message: fmt.Sprintf("Collector template '%s' was not found for Observability Service '%s'.\n\nCreate one from the Connext Cloud dashboard\n  - Open %s\nThen rerun:\n  rticloud gateway", collectorTemplate, stringValue(config, "observability"), DashboardURL(zone, stringValue(config, "observability"), "observability"))}
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
	return pidRunning(pid)
}

func templateListContains(items []TemplateItem, target string) bool {
	for _, item := range items {
		if item.Name == target {
			return true
		}
	}
	return false
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
	return template + "-1"
}

func sortedKeys(values map[string]map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

func localSecureFilesExist(directory string) bool {
	for _, name := range secureFiles {
		if !fileExists(filepath.Join(directory, name)) {
			return false
		}
	}
	return true
}

func saveSecureFiles(files map[string]string, privateKey []byte, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	for filename, content := range files {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(targetDir, filename), decoded, secureFileMode(filename)); err != nil {
			return err
		}
	}
	if len(privateKey) > 0 {
		if err := os.WriteFile(filepath.Join(targetDir, "client.key"), privateKey, secureFileMode("client.key")); err != nil {
			return err
		}
	}
	return nil
}

func secureFileMode(fileName string) os.FileMode {
	if strings.HasSuffix(fileName, ".key") {
		return 0o600
	}
	return 0o644
}

func isSecure(resource map[string]any) bool {
	if resource == nil {
		return false
	}
	config, _ := resource["config"].(map[string]any)
	if secure, ok := config["secure"].(bool); ok && secure {
		return true
	}
	if secure, ok := resource["secure"].(bool); ok {
		return secure
	}
	return false
}

func stringValue(config map[string]any, key string) string {
	if config == nil {
		return ""
	}
	value, _ := config[key].(string)
	return value
}

func nestedString(config map[string]any, keys ...string) string {
	current := any(config)
	for _, key := range keys {
		mapping, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapping[key]
	}
	value, _ := current.(string)
	return value
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pidRunning(pid int) bool {
	return syscall.Kill(pid, 0) == nil
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
