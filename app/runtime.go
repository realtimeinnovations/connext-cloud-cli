// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package app

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/cloudapi"
	"github.com/realtimeinnovations/connext-cloud-cli/commands"
	"github.com/realtimeinnovations/connext-cloud-cli/common"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	mgcrypto "github.com/realtimeinnovations/connext-cloud-cli/crypto"
	"github.com/realtimeinnovations/connext-cloud-cli/edgeprovision"
	"github.com/realtimeinnovations/connext-cloud-cli/edgesyncagent"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	internalconnext "github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/update"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

const (
	downloadConnextLicenseLabel       = "Yes, download a license from evaluation.rti.com"
	manualDownloadConnextLicenseLabel = "Manually download a license"
	cancelConnextLicenseLabel         = "No, skip license download"
	evaluationLicenseURL              = "https://evaluation.rti.com/workspaces/license"
)

type Runtime struct {
	Out           io.Writer
	Config        *config.Manager
	Auth          *auth.Manager
	WorkAuth      *auth.Manager
	CloudAPI      *cloudapi.Client
	Commands      *commands.Runner
	License       *commands.Runner
	Gateway       *gateway.GatewayApp
	Spy           *spy.App
	EdgeProvision *edgeprovision.Runner
	EdgeStore     *edgestore.Store
	EdgeSyncAgent *edgesyncagent.Agent
	Updater       *update.Manager
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
	edgeProvisionRunner := edgeprovision.NewRunner(out)
	edgeStoreRunner := edgestore.New(filepath.Join(workDir, ".connext"))
	commandRunner.EdgeStore = edgeStoreRunner
	agentApp := edgesyncagent.NewAgent(edgeStoreRunner, out)
	agentApp.In = os.Stdin
	agentApp.EnrollFunc = func(serviceID, participantID, serial string, macs []string, csrFile, keyFile, campaignToken string) (string, error) {
		return commandRunner.EnrollDevice(serviceID, participantID, serial, macs, csrFile, keyFile, campaignToken)
	}
	agentApp.EnrollDirectFunc = func(serviceID, domainTemplateID, participantTemplateID, serial string, macs []string, deviceName, csrFile, keyFile string) (string, string, error) {
		return commandRunner.EnrollDeviceDirect(serviceID, domainTemplateID, participantTemplateID, serial, macs, deviceName, csrFile, keyFile)
	}
	agentApp.ListServicesFunc = commandRunner.FetchEdgeSystems
	agentApp.ListDomainTemplatesFunc = commandRunner.FetchDomainTemplates
	agentApp.ListParticipantTemplatesFunc = commandRunner.FetchParticipantTemplates
	// newAgentEdgeRunner builds an edgeprovision.Runner configured for the agent
	// (log-file output + current debug flag).  Each artifact closure below builds
	// one on demand because cert/key/ca/URL come from the per-call store slot.
	newAgentEdgeRunner := func() *edgeprovision.Runner {
		r := edgeprovision.NewRunner(agentApp.LogOut)
		r.Debug = agentApp.Debug
		return r
	}
	agentApp.RequestIdentityFunc = func(url, cert, key, ca, serverAddr, csrFile, output string) error {
		return newAgentEdgeRunner().RequestIdentity(url, cert, key, ca, serverAddr, csrFile, output)
	}
	agentApp.RequestPermissionsFunc = func(url, cert, key, ca, serverAddr, output string) error {
		return newAgentEdgeRunner().RequestPermissions(url, cert, key, ca, serverAddr, output)
	}
	agentApp.RequestPSKFunc = func(url, cert, key, ca, serverAddr, output string) error {
		return newAgentEdgeRunner().RequestPSK(url, cert, key, ca, serverAddr, output)
	}
	agentApp.GetCRLFunc = func(url, cert, key, ca, serverAddr, output string) error {
		return newAgentEdgeRunner().GetCRL(url, cert, key, ca, serverAddr, output)
	}
	agentApp.RenewDeviceCertFunc = func(url, cert, key, ca, serverAddr, csrFile string, validityMinutes int, output string) error {
		return newAgentEdgeRunner().RenewDeviceCert(url, cert, key, ca, serverAddr, csrFile, validityMinutes, output)
	}
	agentApp.GenerateKeyAndCSRFunc = generateAgentKeyAndCSR
	agentApp.GenerateCSRFromKeyFunc = generateAgentCSRFromKey
	updater := update.New(configManager, out)
	return &Runtime{
		Out:           out,
		Config:        configManager,
		Auth:          authManager,
		WorkAuth:      evaluationAuthManager,
		CloudAPI:      cloudClient,
		Commands:      commandRunner,
		License:       licenseRunner,
		Gateway:       gatewayApp,
		Spy:           spyApp,
		EdgeProvision: edgeProvisionRunner,
		EdgeStore:     edgeStoreRunner,
		EdgeSyncAgent: agentApp,
		Updater:       updater,
	}
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

// generateAgentKeyAndCSR generates a fresh ECDSA P-256 private key and a CSR
// for the given common name and organisation, writing both PEM files to tmpDir.
func generateAgentKeyAndCSR(commonName, org, tmpDir string) (keyPath, csrPath string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyPath = filepath.Join(tmpDir, "identity.key")
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return "", "", err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName, Organization: []string{org}},
	}, key)
	if err != nil {
		return "", "", err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	csrPath = filepath.Join(tmpDir, "identity.csr")
	if err := os.WriteFile(csrPath, csrPEM, 0o644); err != nil {
		return "", "", err
	}
	return keyPath, csrPath, nil
}

// generateAgentCSRFromKey creates a CSR from an existing PEM-encoded private
// key, writing the CSR PEM file to tmpDir.  Used for identity renewal so the
// device key is never rotated.
func generateAgentCSRFromKey(commonName, org string, keyPEM []byte, tmpDir string) (csrPath string, err error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return "", fmt.Errorf("no PEM block found in key data")
	}
	var signer crypto.Signer
	switch block.Type {
	case "EC PRIVATE KEY":
		signer, err = x509.ParseECPrivateKey(block.Bytes)
	case "RSA PRIVATE KEY":
		signer, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			signer, ok = parsed.(crypto.Signer)
			if !ok {
				return "", fmt.Errorf("PKCS8 key is not a signer")
			}
		}
	default:
		return "", fmt.Errorf("unsupported PEM block type: %s", block.Type)
	}
	if err != nil {
		return "", fmt.Errorf("parsing private key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName, Organization: []string{org}},
	}, signer)
	if err != nil {
		return "", err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	csrPath = filepath.Join(tmpDir, "identity.csr")
	if err := os.WriteFile(csrPath, csrPEM, 0o644); err != nil {
		return "", err
	}
	return csrPath, nil
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
	selected, err := selectFunc(message, []string{downloadConnextLicenseLabel, manualDownloadConnextLicenseLabel, cancelConnextLicenseLabel})
	if err != nil {
		return err
	}
	if selected == manualDownloadConnextLicenseLabel {
		licensePath := internalconnext.LicenseFilePath(install)
		_, _ = fmt.Fprint(runtime.Out, gateway.RenderKeyValuePanel("• Manually download Connext license:", []gateway.KeyValueRow{
			{Key: "Step 1", Value: "Open the evaluation license page:"},
			{Value: tui.StyleLink(evaluationLicenseURL)},
			{Key: "Step 2", Value: "Download the license file."},
			{Key: "Step 3", Value: fmt.Sprintf("Copy it to %s", licensePath)},
		}))
		return common.UserError{Message: fmt.Sprintf("Copy the license file to %s, then rerun this command.", licensePath)}
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
