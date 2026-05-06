package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/config"
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

func GenerateCodeVerifier(length int) (string, error) {
	if length <= 0 {
		length = 64
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"
	raw := make([]byte, length)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	for idx := range raw {
		raw[idx] = alphabet[int(raw[idx])%len(alphabet)]
	}
	return string(raw), nil
}

func GenerateCodeChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return strings.TrimRight(base64.URLEncoding.EncodeToString(digest[:]), "=")
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
	redirectURI := configValues["redirect_uri"]
	if redirectURI == "" {
		redirectURI = "http://localhost:8000/callback"
	}
	parsedRedirect, err := url.Parse(redirectURI)
	if err != nil {
		return "", err
	}
	verifier, err := GenerateCodeVerifier(64)
	if err != nil {
		return "", err
	}
	state, err := GenerateCodeVerifier(32)
	if err != nil {
		return "", err
	}
	challenge := GenerateCodeChallenge(verifier)
	authParams := url.Values{
		"client_id":             []string{clientID},
		"audience":              []string{audience},
		"response_type":         []string{"code"},
		"redirect_uri":          []string{redirectURI},
		"scope":                 []string{scope},
		"state":                 []string{state},
		"code_challenge":        []string{challenge},
		"code_challenge_method": []string{"S256"},
	}
	authorizationURL := fmt.Sprintf("https://%s/authorize?%s", auth0Domain, authParams.Encode())
	listener, err := net.Listen("tcp", parsedRedirect.Host)
	if err != nil {
		return "", err
	}
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
		requestBody := url.Values{
			"grant_type":    []string{"authorization_code"},
			"client_id":     []string{clientID},
			"code":          []string{result.code},
			"redirect_uri":  []string{redirectURI},
			"code_verifier": []string{verifier},
		}
		response, err := manager.HTTPClient.PostForm(fmt.Sprintf("https://%s/oauth/token", auth0Domain), requestBody)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		var tokenResponse struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			ErrorDesc   string `json:"error_description"`
		}
		if err := json.NewDecoder(response.Body).Decode(&tokenResponse); err != nil {
			return "", err
		}
		if tokenResponse.AccessToken == "" {
			return "", fmt.Errorf("Error: %s", tokenResponse.ErrorDesc)
		}
		if err := manager.SaveAccessToken(tokenResponse.AccessToken, tokenResponse.ExpiresIn); err != nil {
			return "", err
		}
		return tokenResponse.AccessToken, nil
	case <-time.After(5 * time.Minute):
		return "", fmt.Errorf("Error: Did not receive an authorization code in time.")
	}
}

func NewDefaultManager() *Manager {
	return New(config.New(""), "")
}
