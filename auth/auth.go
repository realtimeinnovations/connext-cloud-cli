package auth

import (
	"context"
	"encoding/json"
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
	OpenBrowser BrowserOpener
	Stdout      io.Writer
}

type tokenFile struct {
	AccessToken string `json:"access_token"`
	ExpiresAt   string `json:"expires_at"`
}

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
		OpenBrowser: defaultOpenBrowser,
		Stdout:      os.Stdout,
	}
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
		auth0Domain = "auth.dev-rti.com"
	}
	audience := configValues["audience"]
	if audience == "" {
		audience = "https://cloud.rti.com/api/v1"
	}
	scope := configValues["scope"]
	if scope == "" {
		scope = "openid profile email list:databus query:databus create:databus delete:databus create:databus_client"
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
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, "Invalid state parameter.")
			select {
			case resultCh <- authResult{err: fmt.Errorf("Error: Invalid OAuth state parameter.")}:
			default:
			}
			return
		}
		code := request.URL.Query().Get("code")
		if code == "" {
			writer.WriteHeader(http.StatusBadRequest)
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
	_, _ = fmt.Fprintf(manager.Stdout, "If the browser does not open, visit this URL manually:\n  %s\n", authorizationURL)
	_ = manager.OpenBrowser(authorizationURL)
	select {
	case result := <-resultCh:
		if result.err != nil {
			return "", result.err
		}
		token, err := oauthConfig.Exchange(oauthContext(manager.HTTPClient), result.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return "", err
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
