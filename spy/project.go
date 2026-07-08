package spy

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/prompt"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
	"gopkg.in/yaml.v3"
)

const (
	MinConnextVersion      = "7.7.0"
	RTICloudSpyAppName     = "rticloud_spy"
	RTICloudSpyAppPort     = 7281
	CreateRTICloudSpyApp   = "__create_rticloud_spy_app__"
	CreateRTICloudSpyLabel = "Create rticloud_spy cloud application"
	CreateNewApp           = "__create_new_app__"
	CreateNewAppLabel      = "Create new cloud application..."
	ReloadAppListLabel     = "Reload application list"
	CancelSpySetupLabel    = "Cancel spy setup"
	SpyLogName             = "spy.log"
	spyRenderPollInterval  = 50 * time.Millisecond
	spyLiveRefreshInterval = 100 * time.Millisecond
)

type UserError = common.UserError
type ConnextInstall = connext.Install
type TemplateItem = common.TemplateItem

type App struct {
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
	DownloadArtifactsFunc    func(config map[string]any, force bool) error
	PIDRunningFunc           func(pid int) bool
	SelectFunc               func(message string, choices []string) (string, error)
	InputFunc                func(message string) (string, error)
	ConfirmReloadFunc        func(message string) (bool, error)
	InterruptSignalFunc      func() (<-chan os.Signal, func())
	Now                      func() time.Time
}

type RunOptions struct {
	TextOutput bool
}

func NewApp(workDir string, out io.Writer) *App {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	app := &App{
		WorkDir: workDir,
		In:      os.Stdin,
		Out:     out,
		Now:     time.Now,
	}
	app.SelectFunc = app.defaultSelect
	app.InputFunc = app.defaultInput
	app.ConfirmReloadFunc = app.defaultConfirmReload
	app.PIDRunningFunc = terminal.ProcessRunning
	return app
}

func (app *App) ConfigPath() string {
	return filepath.Join(app.WorkDir, ".connext", "spy.yaml")
}

func (app *App) SpyDir() string {
	return filepath.Join(app.WorkDir, ".connext", "spy")
}

func (app *App) AppDir() string {
	return filepath.Join(app.SpyDir(), "app")
}

func (app *App) RuntimePath() string {
	return filepath.Join(app.SpyDir(), "runtime.json")
}

func (app *App) LogsDir() string {
	return filepath.Join(app.SpyDir(), "logs")
}

func (app *App) SpyLogPath() string {
	return filepath.Join(app.LogsDir(), SpyLogName)
}

func (app *App) ReadConfig() (map[string]any, error) {
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

func (app *App) WriteConfig(config map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(app.ConfigPath()), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(app.ConfigPath(), data, 0o644)
}

func (app *App) RuntimeState() (map[string]any, error) {
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

func (app *App) WriteRuntimeState(state map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(app.RuntimePath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(app.RuntimePath(), data, 0o644)
}

func HasDatabus(config map[string]any) bool {
	return common.StringValue(config, "databus") != "" && common.NestedString(config, "templates", "app") != ""
}

func TemplateItems(resource map[string]any, expectedKind string) []TemplateItem {
	return common.TemplateItems(resource, expectedKind)
}

func SpyExecutable(path string) string {
	return connext.Executable(path, "rtiddsspy")
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

func EnsureEnhancedDDSSpy(install ConnextInstall, selectFunc func(message string, choices []string) (string, error), out io.Writer) error {
	return connext.EnsureEnhancedDDSSpy(install, selectFunc, out)
}

func connextOptions() connext.DiscoveryOptions {
	return connext.DiscoveryOptions{MinVersion: MinConnextVersion, ExecutableName: "rtiddsspy", CommandName: "spy"}
}

func (app *App) ConfigureFirstRun(prompt bool) (map[string]any, error) {
	databuses, _, err := app.listResources()
	if err != nil {
		return nil, err
	}
	if len(databuses) == 0 {
		return nil, UserError{Message: "No Databuses found."}
	}
	_, _ = fmt.Fprint(app.Out, RenderSetupIntro(len(databuses)))
	connext, err := app.discoverConnextInstall(prompt)
	if err != nil {
		return nil, err
	}
	connextMsg := fmt.Sprintf("Using Connext Pro %s at %s", connext.Version, connext.Path)
	if connext.Reason != "" {
		connextMsg += fmt.Sprintf(" (%s)", connext.Reason)
	}
	_, _ = fmt.Fprint(app.Out, RenderInfoMessage(connextMsg))
	databusName, err := app.choose("Select Databus:", common.SortedKeys(databuses))
	if err != nil {
		return nil, err
	}
	databus, err := app.getResource(databusName)
	if err != nil {
		return nil, err
	}
	appTemplates := TemplateItems(databus, "app")
	appName, err := app.selectAppOrCreate(databusName, fmt.Sprintf("Select Cloud Native application from %s:", databusName), appTemplates)
	if err != nil {
		return nil, err
	}
	config := map[string]any{
		"zone":    app.currentZone(),
		"databus": databusName,
		"templates": map[string]any{
			"app": appName,
		},
		"runtime": map[string]any{
			"min_version":  MinConnextVersion,
			"connext_home": connext.Path,
		},
		"clients": map[string]any{
			"app_client_id": common.GenerateClientID(),
		},
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

func (app *App) DownloadArtifacts(config map[string]any, force bool) error {
	if app.DownloadArtifactsFunc != nil {
		return app.DownloadArtifactsFunc(config, force)
	}
	appName := common.NestedString(config, "templates", "app")
	if appName == "" {
		return nil
	}
	target := filepath.Join(app.AppDir(), appName+".xml")
	if force || !common.FileExists(target) {
		if err := app.downloadTemplate(common.StringValue(config, "databus"), appName, target); err != nil {
			return err
		}
		_, _ = fmt.Fprint(app.Out, RenderSuccessMessage(fmt.Sprintf("Downloaded cloud application template: %s", target)))
	}
	return nil
}

func (app *App) downloadTemplate(databusName string, appName string, target string) error {
	if app.APIGet == nil {
		return fmt.Errorf("API getter is not configured")
	}
	payload, err := app.APIGet(fmt.Sprintf("/databuses/%s/applications/%s", databusName, appName))
	if err != nil {
		if isMissingExternalEndpointError(err) {
			return UserError{Message: fmt.Sprintf("Databus '%s' does not have an external endpoint yet, so spy cannot download the cloud application configuration.\n\nStart or resume the Databus and wait for it to be running:\n  rticloud databus resume --name %s\nThen rerun:\n  rticloud spy", databusName, databusName)}
		}
		return err
	}
	clientConfig := payload["client_config"]
	if clientConfig == nil {
		return UserError{Message: fmt.Sprintf("Error: Unexpected application response for '%s'", appName)}
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

func isMissingExternalEndpointError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "doesn't have an external endpoint configured")
}

func (app *App) ValidateConfigResources(config map[string]any) error {
	if !HasDatabus(config) {
		return UserError{Message: "No Databus or Cloud Native application is configured for this spy."}
	}
	databus, err := app.getResource(common.StringValue(config, "databus"))
	if err != nil {
		return err
	}
	if err := app.validateServiceActive(databus, "Databus", common.StringValue(config, "databus"), "databus"); err != nil {
		return err
	}
	appTemplate := common.NestedString(config, "templates", "app")
	if !common.TemplateListContains(TemplateItems(databus, "app"), appTemplate) {
		if appTemplate == RTICloudSpyAppName {
			_, err := app.createRTICloudSpyApp(common.StringValue(config, "databus"))
			return err
		}
		zone := common.StringValue(config, "zone")
		if zone == "" {
			zone = app.currentZone()
		}
		return UserError{Message: fmt.Sprintf("Cloud Native application '%s' was not found for Databus '%s'.\n\nCreate one from the Connext Cloud dashboard\n  - Open %s\nThen rerun:\n  rticloud spy", appTemplate, common.StringValue(config, "databus"), DashboardURL(zone, common.StringValue(config, "databus")))}
	}
	return nil
}

func (app *App) ValidateLocalArtifacts(config map[string]any) error {
	if !HasDatabus(config) {
		return UserError{Message: "No Databus or Cloud Native application is configured for this spy."}
	}
	appName := common.NestedString(config, "templates", "app")
	path := filepath.Join(app.AppDir(), appName+".xml")
	if !common.FileExists(path) {
		return UserError{Message: fmt.Sprintf("Local spy artifact was not found: %s\n\nRun without --skip-preflight to refresh artifacts:\n  rticloud spy", path)}
	}
	return nil
}

func (app *App) LocalSecureArtifacts() bool {
	return common.LocalSecureFilesExist(app.AppDir())
}

func (app *App) validateServiceActive(resource map[string]any, label string, name string, command string) error {
	status := common.StringValue(resource, "status")
	if status == "" {
		status = common.NestedString(resource, "config", "status")
	}
	if status == common.ServiceStatusActive {
		return nil
	}
	if status == "" {
		status = common.ServiceStatusUnknown
	}
	return UserError{Message: serviceStatusErrorMessage(label, name, command, status, "rticloud spy")}
}

func serviceStatusErrorMessage(label string, name string, command string, status string, retryCommand string) string {
	message := RenderWarningMessage(fmt.Sprintf("%s '%s' is %s, not active.", label, name, status))
	switch status {
	case common.ServiceStatusDisabled:
		message += fmt.Sprintf("\nResume it and wait for it to become active:\n  rticloud %s resume --name %s", command, name)
	case common.ServiceStatusCreating:
		message += fmt.Sprintf("\nWait for creation to finish, then check status:\n  rticloud %s query --name %s", command, name)
	case common.ServiceStatusDeleting:
		message += fmt.Sprintf("\nWait for deletion to finish, or configure this spy with another Databus:\n  rticloud %s query --name %s", command, name)
	case common.ServiceStatusError:
		message += fmt.Sprintf("\nInspect the service and resume it after resolving the error:\n  rticloud %s query --name %s\n  rticloud %s resume --name %s", command, name, command, name)
	default:
		message += fmt.Sprintf("\nCheck the service status:\n  rticloud %s query --name %s", command, name)
	}
	message += fmt.Sprintf("\n\nTo skip this check and connect anyway, rerun:\n  %s --skip-preflight", retryCommand)
	return message
}

func (app *App) EnsureSecureArtifacts(config map[string]any) (bool, error) {
	if !HasDatabus(config) {
		return false, nil
	}
	databus, err := app.getResource(common.StringValue(config, "databus"))
	if err != nil {
		return false, err
	}
	if !common.IsSecure(databus) {
		return false, nil
	}
	_, _ = fmt.Fprint(app.Out, RenderInfoMessage("Secure Databus detected."))
	clients, _ := config["clients"].(map[string]any)
	clientID, _ := clients["app_client_id"].(string)
	if clientID == "" {
		clientID = common.GenerateClientID()
	}
	if err := app.ensureSecureCredentials(common.StringValue(config, "databus"), common.NestedString(config, "templates", "app"), clientID, app.AppDir()); err != nil {
		return false, err
	}
	return true, nil
}

func (app *App) ensureSecureCredentials(databusName string, appName string, clientID string, targetDir string) error {
	if common.LocalSecureFilesExist(targetDir) {
		_, _ = fmt.Fprint(app.Out, RenderInfoMessage("Reusing local spy credentials"))
		return nil
	}
	if app.GenerateCSRFunc == nil || app.APIPost == nil {
		return fmt.Errorf("secure credential dependencies are not configured")
	}
	privateKey, csr, err := app.GenerateCSRFunc(databusName, appName, clientID)
	if err != nil {
		return err
	}
	payload, err := app.APIPost(fmt.Sprintf("/databuses/%s/applications/%s/clients", databusName, appName), map[string]any{"client_id": clientID, "csr": csr})
	if err != nil {
		return UserError{Message: fmt.Sprintf("Unable to register secure spy credentials for application '%s'.\n%v", appName, err)}
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
	_, _ = fmt.Fprint(app.Out, RenderSuccessMessage("Registered spy client credentials"))
	_, _ = fmt.Fprint(app.Out, RenderSuccessMessage(fmt.Sprintf("Saved spy credentials to %s", targetDir)))
	return nil
}

func (app *App) Run(config map[string]any, connext ConnextInstall, databusSecure bool) (int, error) {
	return app.RunWithOptions(config, connext, databusSecure, RunOptions{})
}

func (app *App) RunWithOptions(config map[string]any, connext ConnextInstall, databusSecure bool, options RunOptions) (int, error) {
	appName := common.NestedString(config, "templates", "app")
	if appName == "" {
		return 0, UserError{Message: "No Cloud Native application is configured for this spy."}
	}
	xmlPath := filepath.Join(app.AppDir(), appName+".xml")
	qosProfile, err := QosProfileFromXML(xmlPath, appName)
	if err != nil {
		return 0, err
	}
	command := []string{
		SpyExecutable(connext.Path),
		"-domainId", "100",
		"-printSample", "COMPACT",
		"-timeFormat", "FULL",
		"-qosFile", xmlPath,
		"-qosProfile", qosProfile,
	}
	if err := os.MkdirAll(app.LogsDir(), 0o755); err != nil {
		return 0, err
	}
	liveView := NewLiveView(config)
	liveView.DatabusSecure = databusSecure
	renderer := TerminalRenderer{Out: app.Out}
	defer func() { _ = renderer.Finish() }()
	logFile, err := os.OpenFile(app.SpyLogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer logFile.Close()
	_, _ = fmt.Fprintf(logFile, "Running %s\n", formatCommandLine(command))
	wrapped := terminal.PrepareCommand(command)
	cmd := exec.CommandContext(context.Background(), wrapped[0], wrapped[1:]...)
	cmd.Dir = app.AppDir()
	cmd.Env = mergeEnv(os.Environ(), app.spyEnv()...)
	stdout, stderr, err := terminal.StartProcess(cmd)
	if err != nil {
		return 0, err
	}
	defer closeIfNotNil(stdout)
	defer closeIfNotNil(stderr)
	if err := app.WriteRuntimeState(map[string]any{
		"spy_pid":    cmd.Process.Pid,
		"started_at": app.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return 0, err
	}
	spyLines := make(chan string, 128)
	handleLine := func(line string) {
		if strings.TrimSpace(line) == "" {
			return
		}
		if shouldWriteSpyLogLine(line) {
			_, _ = fmt.Fprintf(logFile, "%s [spy] %s\n", app.Now().UTC().Format(time.RFC3339), line)
		}
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
				spyLines <- line
			}
			if force && pending != "" {
				spyLines <- pending
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
		close(spyLines)
	}()
	interrupts, stopInterrupts := app.interruptSignals()
	defer stopInterrupts()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	if !options.TextOutput {
		_ = renderer.Render(liveView.Render(liveView.PulseFrame()))
	}
	renderTicker := time.NewTicker(spyRenderPollInterval)
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
			if spyLines == nil {
				goto done
			}
		case line, ok := <-spyLines:
			if !ok {
				spyLines = nil
				if processExited {
					goto done
				}
				continue
			}
			handleLine(line)
			for {
				select {
				case line, ok = <-spyLines:
					if !ok {
						spyLines = nil
						if processExited {
							goto done
						}
						goto drainedSpyLines
					}
					handleLine(line)
				default:
					goto drainedSpyLines
				}
			}
		drainedSpyLines:
			pendingLiveRefresh = true
			now := app.Now()
			if lastLiveRefresh.IsZero() || now.Sub(lastLiveRefresh) >= spyRenderPollInterval {
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
			} else if pendingLiveRefresh && (lastLiveRefresh.IsZero() || now.Sub(lastLiveRefresh) >= spyLiveRefreshInterval) {
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
			_ = renderer.Finish()
			if interrupted {
				_, _ = fmt.Fprintln(app.Out, "Spy interrupted.")
				app.printRestartHint()
				return 130, nil
			}
			_, _ = fmt.Fprintln(app.Out, "Spy stopped.")
			app.printRestartHint()
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
	if !options.TextOutput {
		_ = renderer.Render(liveView.PrintSnapshot("stopped"))
	}
	_ = renderer.Finish()
	if interrupted {
		_, _ = fmt.Fprintln(app.Out, "Spy interrupted.")
	}
	app.printRestartHint()
	return 0, nil
}

func shouldWriteSpyLogLine(line string) bool {
	return spyDataRE.FindStringSubmatch(line) == nil
}

func (app *App) spyEnv() []string {
	env := []string{"RTI_MONITORING2_ENABLE=false"}
	privateKeyPath := filepath.Join(app.AppDir(), "client.key")
	if common.FileExists(privateKeyPath) {
		env = append(env, "RTI_PRIVATE_KEY_FILE="+privateKeyPath)
	}
	return env
}

func (app *App) Status() error {
	config, err := app.ReadConfig()
	if err != nil {
		return err
	}
	if config == nil {
		_, _ = fmt.Fprintln(app.Out, "No spy configuration found in this project.")
		_, _ = fmt.Fprintln(app.Out, "Run:")
		_, _ = fmt.Fprintln(app.Out, "  rticloud spy")
		return nil
	}
	runtimeState, err := app.RuntimeState()
	if err != nil {
		return err
	}
	app.PrintConfigSummary(config)
	pid := intFromAny(runtimeState["spy_pid"])
	state := "stopped"
	if pid > 0 && app.pidRunning(pid) {
		state = fmt.Sprintf("running (pid %d)", pid)
	} else if pid > 0 {
		state = fmt.Sprintf("stopped (stale pid %d)", pid)
	}
	_, _ = fmt.Fprintf(app.Out, "DDS Spy: %s\n", state)
	connextHome := common.NestedString(config, "runtime", "connext_home")
	if connextHome != "" {
		connext, err := ValidateConnextInstall(connextHome)
		if err != nil {
			_, _ = fmt.Fprintf(app.Out, "Connext: unavailable (%s)\n", connextHome)
		} else {
			_, _ = fmt.Fprintf(app.Out, "Connext: %s (%s)\n", connext.Version, connext.Path)
		}
	}
	return nil
}

func (app *App) Reset() error {
	if !common.FileExists(app.ConfigPath()) {
		_, _ = fmt.Fprintln(app.Out, "No spy configuration found.")
		return nil
	}
	if err := os.Remove(app.ConfigPath()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(app.Out, "Removed %s\n", app.ConfigPath())
	if err := common.RemoveSecureFiles(app.AppDir()); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(app.Out, "Removed spy credentials from %s\n", app.AppDir())
	_, _ = fmt.Fprintln(app.Out)
	_, _ = fmt.Fprintf(app.Out, "Runtime artifacts were left in\nPath: %s\n", app.SpyDir())
	_, _ = fmt.Fprintf(app.Out, "Logs were left in\nPath: %s\n", app.LogsDir())
	return nil
}

func (app *App) PrintConfigSummary(config map[string]any) {
	_, _ = fmt.Fprintln(app.Out, "Spy configuration:")
	_, _ = fmt.Fprintf(app.Out, "  Databus: %s\n", fallbackString(common.StringValue(config, "databus"), "not configured"))
	_, _ = fmt.Fprintf(app.Out, "  Cloud Native application: %s\n", fallbackString(common.NestedString(config, "templates", "app"), "not configured"))
	_, _ = fmt.Fprintln(app.Out)
}

func (app *App) printRestartHint() {
	_, _ = fmt.Fprintf(app.Out, "• Logs saved under %s\n", app.LogsDir())
	_, _ = fmt.Fprintln(app.Out, "• Run 'rticloud spy' from this directory to start this spy again.")
}

func QosProfileFromXML(xmlPath string, appName string) (string, error) {
	data, err := os.ReadFile(xmlPath)
	if err != nil {
		return "", err
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	library := ""
	currentLibrary := ""
	firstLibrary := ""
	firstProfile := ""
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "qos_library":
			currentLibrary = attr(start, "name")
			if firstLibrary == "" {
				firstLibrary = currentLibrary
			}
		case "qos_profile":
			profile := attr(start, "name")
			if firstProfile == "" && currentLibrary != "" {
				firstLibrary = currentLibrary
				firstProfile = profile
			}
			if attr(start, "is_default_qos") == "true" && currentLibrary != "" && profile != "" {
				return currentLibrary + "::" + profile, nil
			}
			if profile == appName+"_qos_profile" && currentLibrary != "" {
				library = currentLibrary
			}
		}
	}
	if library != "" {
		return library + "::" + appName + "_qos_profile", nil
	}
	if firstLibrary != "" && firstProfile != "" {
		return firstLibrary + "::" + firstProfile, nil
	}
	return "", UserError{Message: fmt.Sprintf("Unable to find a QoS profile in %s", xmlPath)}
}

func attr(element xml.StartElement, name string) string {
	for _, attr := range element.Attr {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func RenderSetupIntro(databusCount int) string {
	rows := []string{
		"Connext Cloud Spy setup",
		"Create a project-local spy configuration for this workspace.",
		"",
		fmt.Sprintf("Databuses available: %d", databusCount),
		"",
		"Use arrow keys to choose and Enter to confirm.",
	}
	width := 0
	for _, row := range rows {
		if rowWidth := tui.DisplayWidth(row); rowWidth > width {
			width = rowWidth
		}
	}
	top := "╭" + strings.Repeat("─", width+2) + "╮"
	bottom := "╰" + strings.Repeat("─", width+2) + "╯"
	lines := []string{"", "\x1b[38;5;110m" + top + "\x1b[0m"}
	for index, row := range rows {
		content := tui.PadDisplay(row, width)
		switch index {
		case 0:
			content = tui.StyleTitle(content)
		case 1, 5:
			content = tui.Dim(content)
		}
		lines = append(lines, fmt.Sprintf("\x1b[38;5;110m│\x1b[0m %s \x1b[38;5;110m│\x1b[0m", content))
	}
	lines = append(lines, "\x1b[38;5;110m"+bottom+"\x1b[0m", "")
	return strings.Join(lines, "\n")
}

func RenderInfoMessage(message string) string {
	return fmt.Sprintf("\x1b[38;5;110m•\x1b[0m %s\n", message)
}

func RenderWarningMessage(message string) string {
	return fmt.Sprintf("\x1b[33m⚠\x1b[0m %s\n", message)
}

func RenderSuccessMessage(message string) string {
	return fmt.Sprintf("\x1b[32m✓\x1b[0m %s\n", message)
}

func DashboardURL(zone string, resourceName string) string {
	host := ""
	scheme := "https"
	switch zone {
	case "dev-local":
		host = "localhost:8080"
		scheme = "http"
	case "dev-cloud":
		host = "test.cloud.dev-rti.com"
	case "us-east-2":
		host = "cloud.rti.com"
	default:
		host = zone + ".cloud.rti.com"
	}
	return fmt.Sprintf("%s://%s/dashboard/databuses/%s", scheme, host, resourceName)
}

func (app *App) selectAppOrCreate(databusName string, selectMessage string, templates []TemplateItem) (string, error) {
	for {
		hasSpyApp := false
		choices := make([]string, 0, len(templates)+2)
		for _, item := range templates {
			choices = append(choices, item.Name)
			if item.Name == RTICloudSpyAppName {
				hasSpyApp = true
			}
		}
		if !hasSpyApp {
			choices = append(choices, CreateRTICloudSpyApp)
		}
		choices = append(choices, CreateNewApp)
		selected, err := app.choose(selectMessage, choices)
		if err != nil {
			return "", err
		}
		if selected == CreateRTICloudSpyApp {
			return app.createRTICloudSpyApp(databusName)
		}
		if selected != CreateNewApp {
			return selected, nil
		}
		_, templates, err = app.waitForAppCreation(databusName)
		if err != nil {
			return "", err
		}
	}
}

func (app *App) createRTICloudSpyApp(databusName string) (string, error) {
	if app.APIPost == nil {
		return "", fmt.Errorf("API not configured")
	}
	_, err := app.APIPost(fmt.Sprintf("/databuses/%s/applications", databusName), map[string]any{
		"kind":        "app",
		"client_name": RTICloudSpyAppName,
		"port":        RTICloudSpyAppPort,
		"topic_data": map[string]any{
			"0": map[string]any{
				"domainId":      0,
				"tag":           "",
				"configuration": "all",
				"allTopicsConfiguration": map[string]any{
					"cloudToEdgeDirection": true,
					"cloudToEdgeHistory":   1,
					"edgeToCloudDirection": false,
					"edgeToCloudHistory":   1,
				},
			},
		},
	})
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprint(app.Out, RenderSuccessMessage("Created rticloud_spy cloud application with subscribe-only access to all topics"))
	return RTICloudSpyAppName, nil
}

func (app *App) waitForAppCreation(databusName string) (map[string]any, []TemplateItem, error) {
	zone := app.currentZone()
	dashboard := DashboardURL(zone, databusName)
	for {
		_, _ = fmt.Fprintf(app.Out, "• Create Cloud Native application in Connext Cloud dashboard:\n  %s\n\n", dashboard)
		confirm, err := app.confirmReload("After you've created the Cloud Native application in the dashboard, reload.")
		if err != nil {
			return nil, nil, err
		}
		if !confirm {
			return nil, nil, UserError{Message: "Spy configuration cancelled."}
		}
		_, _ = fmt.Fprint(app.Out, RenderInfoMessage("Reloading applications..."))
		resource, err := app.getResource(databusName)
		if err != nil {
			return nil, nil, err
		}
		templates := TemplateItems(resource, "app")
		if len(templates) > 0 {
			return resource, templates, nil
		}
	}
}

func (app *App) getResource(name string) (map[string]any, error) {
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

func (app *App) listResources() (map[string]map[string]any, map[string]map[string]any, error) {
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

func (app *App) currentZone() string {
	if app.CurrentZoneFunc != nil {
		return app.CurrentZoneFunc()
	}
	return "unknown"
}

func (app *App) discoverConnextInstall(prompt bool) (ConnextInstall, error) {
	if app.DiscoverConnextInstallFn == nil {
		return ConnextInstall{}, fmt.Errorf("Connext discovery is not configured")
	}
	return app.DiscoverConnextInstallFn(prompt)
}

func (app *App) choose(message string, choices []string) (string, error) {
	if app.SelectFunc == nil {
		return "", fmt.Errorf("interactive selection is not configured")
	}
	return app.SelectFunc(message, choices)
}

func (app *App) confirmReload(message string) (bool, error) {
	if app.ConfirmReloadFunc == nil {
		return false, fmt.Errorf("interactive confirmation is not configured")
	}
	return app.ConfirmReloadFunc(message)
}

func (app *App) defaultSelect(message string, choices []string) (string, error) {
	return app.selector().Select(message, choices)
}

func (app *App) defaultInput(message string) (string, error) {
	return prompt.Input{In: app.input(), Out: app.Out, CancelMessage: "Spy configuration cancelled."}.Prompt(message)
}

func (app *App) defaultConfirmReload(message string) (bool, error) {
	selection, err := app.defaultSelect(message, []string{ReloadAppListLabel, CancelSpySetupLabel})
	if err != nil {
		return false, err
	}
	return selection == ReloadAppListLabel, nil
}

func (app *App) input() io.Reader {
	if app.In != nil {
		return app.In
	}
	return os.Stdin
}

func (app *App) selector() prompt.Selector {
	return prompt.Selector{
		In:            app.input(),
		Out:           app.Out,
		CancelMessage: "Spy configuration cancelled.",
		SpecialLabels: map[string]string{
			CreateRTICloudSpyApp: CreateRTICloudSpyLabel,
			CreateNewApp:         CreateNewAppLabel,
		},
	}
}

func (app *App) interruptSignals() (<-chan os.Signal, func()) {
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

func (app *App) pidRunning(pid int) bool {
	if app.PIDRunningFunc != nil {
		return app.PIDRunningFunc(pid)
	}
	return terminal.ProcessRunning(pid)
}

func formatCommandLine(command []string) string {
	quoted := make([]string, 0, len(command))
	for _, arg := range command {
		quoted = append(quoted, quoteCommandArg(arg))
	}
	return strings.Join(quoted, " ")
}

func quoteCommandArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\"'\\") {
		return arg
	}
	return `"` + strings.ReplaceAll(arg, `"`, `\"`) + `"`
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

func closeIfNotNil(closer io.Closer) {
	if closer != nil {
		_ = closer.Close()
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	default:
		return 0
	}
}
