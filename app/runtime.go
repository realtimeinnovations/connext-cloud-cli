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
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	mgcrypto "github.com/realtimeinnovations/connext-cloud-cli/crypto"
	"github.com/realtimeinnovations/connext-cloud-cli/edgeprovision"
	"github.com/realtimeinnovations/connext-cloud-cli/edgesyncagent"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

type Runtime struct {
	Out           io.Writer
	Config        *config.Manager
	Auth          *auth.Manager
	CloudAPI      *cloudapi.Client
	Commands      *commands.Runner
	Gateway       *gateway.GatewayApp
	Spy           *spy.App
	EdgeProvision *edgeprovision.Runner
	EdgeStore     *edgestore.Store
	EdgeSyncAgent *edgesyncagent.Agent
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
	edgeProvisionRunner := edgeprovision.NewRunner(out)
	edgeStoreRunner := edgestore.New(filepath.Join(workDir, ".connext"))
	commandRunner.EdgeStore = edgeStoreRunner
	agentApp := edgesyncagent.NewAgent(edgeStoreRunner, out)
	agentApp.In = os.Stdin
	agentApp.EnrollFunc = func(serviceID, participantID, serial string, macs []string, csrFile, keyFile, campaignToken string) (string, error) {
		return commandRunner.EnrollDevice(serviceID, participantID, serial, macs, csrFile, keyFile, campaignToken)
	}
	agentApp.DeriveDeviceURLFunc = func(serviceID string) string {
		return DeriveDeviceURL(configManager.GetAPIURLSafe(), serviceID)
	}
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
	return &Runtime{
		Out:           out,
		Config:        configManager,
		Auth:          authManager,
		CloudAPI:      cloudClient,
		Commands:      commandRunner,
		Gateway:       gatewayApp,
		Spy:           spyApp,
		EdgeProvision: edgeProvisionRunner,
		EdgeStore:     edgeStoreRunner,
		EdgeSyncAgent: agentApp,
	}
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

// DeriveDeviceURL constructs the device endpoint base URL from the Manager API
// host URL and the service namespace.  The "ces-" naming prefix is stripped
// from the service namespace (Kubernetes convention for the edge-service).
//
//	API host                              → device URL
//	https://test.cloud.dev-rti.com/…     → https://<svc>.devices.cloud.dev-rti.com
//	https://us-west-2.cloud.dev-rti.com  → https://<svc>.devices.cloud.dev-rti.com
//	http://localhost:8090                 → https://<svc>.devices.cloud.dev-rti.com (dev-local fallback)
func DeriveDeviceURL(apiHost, serviceID string) string {
	h := apiHost
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/:"); i >= 0 {
		h = h[:i]
	}
	const marker = ".cloud."
	cloudDomain := ""
	if idx := strings.Index(h, marker); idx >= 0 {
		cloudDomain = h[idx+1:]
	} else {
		cloudDomain = "cloud.dev-rti.com"
	}
	name := strings.TrimPrefix(serviceID, "ces-")
	return "https://" + name + ".devices." + cloudDomain
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

func (runtime *Runtime) RunSpy(format string) error {
	configValues, err := runtime.Spy.ReadConfig()
	if err != nil {
		return err
	}
	if configValues == nil {
		configValues, err = runtime.Spy.ConfigureFirstRun(!runtime.liveTextOutput(format))
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
	if err := spy.EnsureEnhancedDDSSpy(connext, runtime.Spy.SelectFunc, runtime.Out); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runtime.Out, "Connext Pro %s found at %s\n", connext.Version, connext.Path)
	_, err = runtime.Spy.RunWithOptions(configValues, connext, databusSecure, spy.RunOptions{TextOutput: runtime.liveTextOutput(format)})
	return err
}

func (runtime *Runtime) RunGateway(format string) error {
	configValues, err := runtime.Gateway.ReadConfig()
	if err != nil {
		return err
	}
	if configValues == nil {
		configValues, err = runtime.Gateway.ConfigureFirstRun(!runtime.liveTextOutput(format))
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
