package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
	"golang.org/x/oauth2"
)

type ConfigProvider interface {
	GetConfig() (map[string]string, error)
	GetClientID() string
	RequireConfiguration(out io.Writer) bool
}

type BrowserOpener func(string) error

type Manager struct {
	Config      ConfigProvider
	TokenPath   string
	HTTPClient  *http.Client
	Env         func(string) string
	Now         func() time.Time
	Sleep       func(time.Duration)
	OpenBrowser BrowserOpener
	Stdout      io.Writer
}

type tokenFile struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   string `json:"expires_at"`
}

type fixedConfigProvider struct {
	values   map[string]string
	clientID string
}

func (provider fixedConfigProvider) GetConfig() (map[string]string, error) {
	configValues := map[string]string{}
	for key, value := range provider.values {
		configValues[key] = value
	}
	return configValues, nil
}

func (provider fixedConfigProvider) GetClientID() string {
	return provider.clientID
}

func (fixedConfigProvider) RequireConfiguration(io.Writer) bool {
	return true
}

const (
	EvaluationBaseURL         = "https://evaluation.rti.com"
	devLocalDeviceAuthBaseURL = "http://localhost:8080/api/v1"
	workspacesAuth0Domain     = "auth.rti.com"
	workspacesAuth0Audience   = "https://workspaces.cloud.rti.com/api/v1"
	workspacesAuth0Scope      = "openid profile email read:workspace create:workspace basic_access"
	workspacesCredentialsFile = "workspaces_credentials.json"
)

const authSuccessHTML = `<html>
	<head>
		<title>Authentication Succeeded</title>
		<style>
			body {
				font-family: Arial, sans-serif;
				text-align: center;
				padding: 50px;
				background-color: #f0f8ff;
			}
			.container {
				background-color: white;
				padding: 30px;
				border-radius: 10px;
				box-shadow: 0 4px 8px rgba(0, 0, 0, 0.1);
				display: inline-block;
				width: 45%;
				max-width: 600px;
			}
			img { width: 100px; }
		</style>
	</head>
	<body>
		<div class="container">
			<img src="https://avatars.githubusercontent.com/u/3244894?s=200&v=4" alt="RTI logo">
			<h1>Authentication Succeeded</h1>
			<p>You may now close this tab and return to the terminal.</p>
		</div>
	</body>
</html>`

func New(configProvider ConfigProvider, tokenPath string) *Manager {
	if tokenPath == "" {
		tokenPath = config.DefaultCredentialsPath()
	}
	return &Manager{
		Config:      configProvider,
		TokenPath:   tokenPath,
		HTTPClient:  &http.Client{Timeout: 30 * time.Second},
		Env:         os.Getenv,
		Now:         time.Now,
		Sleep:       time.Sleep,
		OpenBrowser: defaultOpenBrowser,
		Stdout:      os.Stdout,
	}
}

func NewEvaluationManager(tokenPath string) *Manager {
	return NewEvaluationManagerWithEnv(tokenPath, os.Getenv)
}

func NewEvaluationManagerWithEnv(tokenPath string, env func(string) string) *Manager {
	if tokenPath == "" {
		tokenPath = DefaultWorkspacesCredentialsPath()
	}
	manager := New(fixedConfigProvider{
		clientID: config.GetWorkspacesClientID(env),
		values: map[string]string{
			"api_host":     EvaluationBaseURL + "/api/v1",
			"auth0_domain": workspacesAuth0Domain,
			"audience":     workspacesAuth0Audience,
			"scope":        workspacesAuth0Scope,
		},
	}, tokenPath)
	manager.Env = func(string) string { return "" }
	return manager
}

func DefaultWorkspacesCredentialsPath() string {
	return filepath.Join(config.DefaultDir(), workspacesCredentialsFile)
}

func EvaluationAPIURL() (string, error) {
	return EvaluationBaseURL + "/api/v1", nil
}

func defaultOpenBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.Command("xdg-open", target)
	}
	return command.Start()
}

func (manager *Manager) GetAccessTokenFromHomeFile() (string, error) {
	data, err := os.ReadFile(manager.TokenPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var token tokenFile
	if err := json.Unmarshal(data, &token); err != nil {
		_ = os.Remove(manager.TokenPath)
		return "", nil
	}
	if token.AccessToken == "" || token.ExpiresAt == "" {
		_ = os.Remove(manager.TokenPath)
		return "", nil
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, token.ExpiresAt)
	if err != nil {
		_ = os.Remove(manager.TokenPath)
		return "", nil
	}
	if manager.Now().Before(expiresAt) {
		return token.AccessToken, nil
	}
	_ = os.Remove(manager.TokenPath)
	return "", nil
}

func (manager *Manager) SaveAccessToken(token string, expiresIn int) error {
	expiresAt := manager.Now().Add(time.Hour)
	if expiresIn > 0 {
		expiresAt = manager.Now().Add(time.Duration(expiresIn-15) * time.Second)
	}
	data, err := json.MarshalIndent(tokenFile{AccessToken: token, ExpiresAt: expiresAt.Format(time.RFC3339Nano)}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(manager.TokenPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(manager.TokenPath, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(manager.TokenPath, 0o600)
}

func (manager *Manager) Logout() error {
	if err := os.Remove(manager.TokenPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (manager *Manager) GetAccessTokenFromAPIKey(apiKey string, apiURL string) (string, int, error) {
	request, err := http.NewRequest(http.MethodPost, strings.TrimRight(apiURL, "/")+"/service-accounts/auth/token", nil)
	if err != nil {
		return "", 0, err
	}
	request.Header.Set("X-API-Key", apiKey)
	response, err := manager.HTTPClient.Do(request)
	if err != nil {
		return "", 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return "", 0, fmt.Errorf("Error authenticating with API key: %d - %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", 0, err
	}
	return payload.AccessToken, payload.ExpiresIn, nil
}

func (manager *Manager) GetAccessTokenForCLI() (string, error) {
	token, err := manager.GetAccessTokenFromHomeFile()
	if err != nil {
		return "", err
	}
	if token != "" {
		return token, nil
	}
	configValues, err := manager.Config.GetConfig()
	if err != nil {
		return "", err
	}
	if apiKey := manager.Env("CONNEXT_CLOUD_API_KEY"); apiKey != "" {
		accessToken, expiresIn, err := manager.GetAccessTokenFromAPIKey(apiKey, configValues["api_host"])
		if err != nil {
			return "", err
		}
		if accessToken == "" {
			return "", fmt.Errorf("Error authenticating with API key: access token missing from response")
		}
		if err := manager.SaveAccessToken(accessToken, expiresIn); err != nil {
			return "", err
		}
		return accessToken, nil
	}
	return manager.Login()
}

func (manager *Manager) GetAuthHeaders() (map[string]string, error) {
	accessToken, err := manager.GetAccessTokenForCLI()
	if err != nil {
		return nil, err
	}
	if accessToken == "" {
		return nil, fmt.Errorf("Authentication required")
	}
	return map[string]string{"Authorization": "Bearer " + accessToken}, nil
}

func (manager *Manager) Login() (string, error) {
	if !manager.Config.RequireConfiguration(manager.Stdout) {
		return "", nil
	}
	configValues, err := manager.Config.GetConfig()
	if err != nil {
		return "", err
	}
	clientID := manager.Config.GetClientID()
	if clientID == "" {
		return "", fmt.Errorf("Error: Client ID not available. This build is missing the Auth0 client ID; set CONNEXT_CLOUD_CLI_CLIENT_ID for development or fix the release packaging.")
	}
	auth0Domain := configValues["auth0_domain"]
	if auth0Domain == "" {
		auth0Domain = defaultAuth0Domain(configValues)
	}
	audience := configValues["audience"]
	if audience == "" {
		audience = defaultAuth0Audience()
	}
	scope := configValues["scope"]
	if scope == "" {
		scope = defaultAuth0Scope()
	}
	listener, redirectURI, err := listenForCallback()
	if err != nil {
		return "", err
	}

	oauthConfig := oauth2.Config{
		ClientID:    clientID,
		RedirectURL: redirectURI,
		Scopes:      strings.Fields(scope),
		Endpoint: oauth2.Endpoint{
			AuthURL:   fmt.Sprintf("https://%s/authorize", auth0Domain),
			TokenURL:  fmt.Sprintf("https://%s/oauth/token", auth0Domain),
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	verifier := oauth2.GenerateVerifier()
	state := oauth2.GenerateVerifier()
	authorizationURL := oauthConfig.AuthCodeURL(
		state,
		oauth2.SetAuthURLParam("audience", audience),
		oauth2.S256ChallengeOption(verifier),
	)
	defer listener.Close()
	type authResult struct {
		code string
		err  error
	}
	resultCh := make(chan authResult, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("state") != state {
			writeCallbackError(writer, "Invalid state parameter.")
			select {
			case resultCh <- authResult{err: fmt.Errorf("Error: Invalid OAuth state parameter.")}:
			default:
			}
			return
		}
		if oauthErr := strings.TrimSpace(request.URL.Query().Get("error")); oauthErr != "" {
			detail := strings.TrimSpace(request.URL.Query().Get("error_description"))
			message := oauthErr
			if detail != "" {
				message += ": " + detail
			}
			writeCallbackError(writer, "OAuth authorization failed. Return to the terminal for details.")
			select {
			case resultCh <- authResult{err: fmt.Errorf("Error: OAuth authorization failed: %s", message)}:
			default:
			}
			return
		}
		code := request.URL.Query().Get("code")
		if code == "" {
			writeCallbackError(writer, "Missing authorization code.")
			select {
			case resultCh <- authResult{err: fmt.Errorf("Error: OAuth authorization response did not include a code.")}:
			default:
			}
			return
		}
		resultCh <- authResult{code: code}
		writer.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(writer, authSuccessHTML)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()
	_, _ = fmt.Fprintln(manager.Stdout, "Opening browser for login...")
	_, _ = fmt.Fprintln(manager.Stdout, "If the browser does not open, or you're logging in on a remote machine, run: rticloud login --device")
	_ = manager.OpenBrowser(authorizationURL)
	select {
	case result := <-resultCh:
		if result.err != nil {
			return "", result.err
		}
		token, err := oauthConfig.Exchange(oauthContext(manager.HTTPClient), result.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return "", formatOAuthExchangeError(err)
		}
		if token.AccessToken == "" {
			return "", fmt.Errorf("Error: OAuth token response did not include an access token")
		}
		if err := manager.SaveAccessToken(token.AccessToken, tokenExpiresIn(token)); err != nil {
			return "", err
		}
		return token.AccessToken, nil
	case <-time.After(5 * time.Minute):
		return "", fmt.Errorf("Error: Did not receive an authorization code in time.")
	}
}

type deviceAuthorizationRequest struct {
	ClientID string `json:"client_id"`
	Audience string `json:"audience"`
	Scope    string `json:"scope"`
}

type deviceAuthorizationResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type deviceTokenRequest struct {
	DeviceCode string `json:"device_code"`
	ClientID   string `json:"client_id"`
}

type deviceTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (manager *Manager) LoginWithDeviceFlow() (string, error) {
	if !manager.Config.RequireConfiguration(manager.Stdout) {
		return "", nil
	}
	configValues, err := manager.Config.GetConfig()
	if err != nil {
		return "", err
	}
	apiHost := strings.TrimRight(configValues["api_host"], "/")
	if apiHost == "" {
		return "", fmt.Errorf("Error: API host is not configured. Run 'rticloud configure' first.")
	}
	deviceBaseURL := deviceAuthBaseURL(apiHost)
	clientID := manager.Config.GetClientID()
	if clientID == "" {
		return "", fmt.Errorf("Error: Client ID not available. This build is missing the Auth0 client ID; set CONNEXT_CLOUD_CLI_CLIENT_ID for development or fix the release packaging.")
	}
	audience := configValues["audience"]
	if audience == "" {
		audience = defaultAuth0Audience()
	}
	scope := configValues["scope"]
	if scope == "" {
		scope = defaultAuth0Scope()
	}

	authorization, err := manager.startDeviceAuthorization(deviceBaseURL, deviceAuthorizationRequest{
		ClientID: clientID,
		Audience: audience,
		Scope:    scope,
	})
	if err != nil {
		return "", err
	}
	if err := validateDeviceAuthorizationResponse(authorization); err != nil {
		return "", err
	}
	verificationURI := deviceAuthURL(authorization.VerificationURI)
	verificationURIComplete := deviceAuthURL(authorization.VerificationURIComplete)

	_, _ = fmt.Fprintln(manager.Stdout, "Attempting to automatically open the SSO authorization page in your default browser.")
	_, _ = fmt.Fprintln(manager.Stdout, "If the browser does not open or you wish to use a different device to authorize this request, open the following URL:")
	_, _ = fmt.Fprintln(manager.Stdout)
	_, _ = fmt.Fprintf(manager.Stdout, "  %s\n", verificationURI)
	_, _ = fmt.Fprintln(manager.Stdout)
	_, _ = fmt.Fprintln(manager.Stdout, "Then enter the code:")
	_, _ = fmt.Fprintln(manager.Stdout)
	_, _ = fmt.Fprintf(manager.Stdout, "  %s\n", strings.ToUpper(authorization.UserCode))
	_ = manager.OpenBrowser(verificationURIComplete)

	interval := deviceFlowInterval(authorization.Interval)
	deadline := manager.Now().Add(time.Duration(authorization.ExpiresIn) * time.Second)
	for {
		if !manager.Now().Before(deadline) {
			return "", deviceFlowExpiredError()
		}
		manager.sleep(interval)
		if !manager.Now().Before(deadline) {
			return "", deviceFlowExpiredError()
		}
		payload, err := manager.pollDeviceToken(deviceBaseURL, deviceTokenRequest{DeviceCode: authorization.DeviceCode, ClientID: clientID})
		if err != nil {
			return "", err
		}
		switch strings.TrimSpace(payload.Error) {
		case "":
			if payload.AccessToken == "" {
				return "", fmt.Errorf("Error: Device authorization token response did not include an access token")
			}
			if err := manager.SaveAccessToken(payload.AccessToken, payload.ExpiresIn); err != nil {
				return "", err
			}
			return payload.AccessToken, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied":
			return "", fmt.Errorf("Error: Device authorization was denied.")
		case "expired_token":
			return "", deviceFlowExpiredError()
		default:
			if detail := strings.TrimSpace(payload.ErrorDescription); detail != "" {
				return "", fmt.Errorf("Error: Device authorization failed: %s: %s", payload.Error, detail)
			}
			return "", fmt.Errorf("Error: Device authorization failed: %s", payload.Error)
		}
	}
}

func (manager *Manager) startDeviceAuthorization(apiHost string, payload deviceAuthorizationRequest) (deviceAuthorizationResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return deviceAuthorizationResponse{}, err
	}
	request, err := http.NewRequest(http.MethodPost, apiHost+"/auth/device", bytes.NewReader(body))
	if err != nil {
		return deviceAuthorizationResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := manager.HTTPClient.Do(request)
	if err != nil {
		return deviceAuthorizationResponse{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return deviceAuthorizationResponse{}, err
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound {
			return deviceAuthorizationResponse{}, fmt.Errorf("Error: Device authorization not available on this Connext Cloud server")
		}
		return deviceAuthorizationResponse{}, fmt.Errorf("Error: Device authorization failed: %s", httputil.FormatError(response.StatusCode, responseBody))
	}
	var authorization deviceAuthorizationResponse
	if err := json.Unmarshal(responseBody, &authorization); err != nil {
		return deviceAuthorizationResponse{}, err
	}
	return authorization, nil
}

func (manager *Manager) pollDeviceToken(apiHost string, payload deviceTokenRequest) (deviceTokenResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return deviceTokenResponse{}, err
	}
	request, err := http.NewRequest(http.MethodPost, apiHost+"/auth/device/token", bytes.NewReader(body))
	if err != nil {
		return deviceTokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := manager.HTTPClient.Do(request)
	if err != nil {
		return deviceTokenResponse{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return deviceTokenResponse{}, err
	}
	var token deviceTokenResponse
	if err := json.Unmarshal(responseBody, &token); err != nil {
		if response.StatusCode != http.StatusOK {
			return deviceTokenResponse{}, fmt.Errorf("Error: Device authorization failed: %s", httputil.FormatError(response.StatusCode, responseBody))
		}
		return deviceTokenResponse{}, err
	}
	if response.StatusCode != http.StatusOK && !isDeviceFlowPollError(token.Error) {
		return deviceTokenResponse{}, fmt.Errorf("Error: Device authorization failed: %s", httputil.FormatError(response.StatusCode, responseBody))
	}
	return token, nil
}

func isDeviceFlowPollError(code string) bool {
	switch strings.TrimSpace(code) {
	case "authorization_pending", "slow_down", "access_denied", "expired_token":
		return true
	default:
		return false
	}
}

func deviceAuthBaseURL(apiHost string) string {
	trimmed := strings.TrimRight(apiHost, "/")
	if trimmed == config.RegionURLMap["dev-local"] {
		return devLocalDeviceAuthBaseURL
	}
	return trimmed
}

func deviceAuthURL(rawURL string) string {
	trimmed := strings.TrimRight(rawURL, "/")
	if suffix, ok := strings.CutPrefix(trimmed, config.RegionURLMap["dev-local"]); ok && (suffix == "" || strings.HasPrefix(suffix, "/") || strings.HasPrefix(suffix, "?")) {
		return devLocalDeviceAuthBaseURL + suffix
	}
	return trimmed
}

func validateDeviceAuthorizationResponse(response deviceAuthorizationResponse) error {
	if response.DeviceCode == "" || response.UserCode == "" || response.VerificationURI == "" || response.VerificationURIComplete == "" || response.ExpiresIn <= 0 {
		return fmt.Errorf("Error: Device authorization response was missing required fields.")
	}
	return nil
}

func deviceFlowInterval(seconds int) time.Duration {
	if seconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func (manager *Manager) sleep(duration time.Duration) {
	if manager.Sleep != nil {
		manager.Sleep(duration)
		return
	}
	time.Sleep(duration)
}

func deviceFlowExpiredError() error {
	return fmt.Errorf("Error: Device authorization expired. Run 'rticloud login --device' again.")
}

func defaultAuth0Audience() string {
	return "https://cloud.rti.com/api/v1"
}

func defaultAuth0Scope() string {
	return "openid profile email list:databus query:databus create:databus delete:databus create:databus_client create:workspace"
}

func defaultAuth0Domain(configValues map[string]string) string {
	apiHost := configValues["api_host"]
	if strings.Contains(apiHost, "cloud.dev-rti.com") || apiHost == config.RegionURLMap["dev-local"] {
		return "auth.dev-rti.com"
	}
	return "auth.rti.com"
}

func writeCallbackError(writer http.ResponseWriter, message string) {
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusBadRequest)
	_, _ = io.WriteString(writer, message)
}

func formatOAuthExchangeError(err error) error {
	var retrieveErr *oauth2.RetrieveError
	if !errors.As(err, &retrieveErr) {
		return err
	}
	parts := []string{}
	if strings.TrimSpace(retrieveErr.ErrorCode) != "" {
		parts = append(parts, strings.TrimSpace(retrieveErr.ErrorCode))
	}
	if strings.TrimSpace(retrieveErr.ErrorDescription) != "" {
		parts = append(parts, strings.TrimSpace(retrieveErr.ErrorDescription))
	}
	if len(parts) > 0 {
		return fmt.Errorf("Error: OAuth token exchange failed: %s", strings.Join(parts, ": "))
	}
	if text := strings.TrimSpace(string(retrieveErr.Body)); text != "" {
		return fmt.Errorf("Error: OAuth token exchange failed: %s", text)
	}
	return fmt.Errorf("Error: OAuth token exchange failed: %v", err)
}

// listenForCallback binds a TCP listener for the OAuth callback.
// It tries the port in parsedRedirect first, then falls back to ports
// 3001–3003 (all registered in Auth0). Returns the listener and the
// redirect URI that matches the bound port.
func listenForCallback() (net.Listener, string, error) {
	ports := []int{3002, 3003, 31810, 31811}
	for _, p := range ports {
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			return l, fmt.Sprintf("http://localhost:%d/callback", p), nil
		}
	}
	ps := make([]string, len(ports))
	for i, p := range ports {
		ps[i] = fmt.Sprintf("%d", p)
	}
	return nil, "", fmt.Errorf("Login failed: ports %s are all in use. Close any other login attempts or applications using these ports and try again.", strings.Join(ps, ", "))
}

func oauthContext(client *http.Client) context.Context {
	ctx := context.Background()
	if client != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	}
	return ctx
}

func tokenExpiresIn(token *oauth2.Token) int {
	if token.ExpiresIn > 0 {
		return int(token.ExpiresIn)
	}
	if !token.Expiry.IsZero() {
		return int(time.Until(token.Expiry).Seconds())
	}
	return 0
}

func NewDefaultManager() *Manager {
	return New(config.New(""), "")
}
