package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/buildinfo"
)

const (
	DefaultAPIURL      = "https://api.github.com/repos/realtimeinnovations/connext-cloud-cli"
	DefaultDownloadURL = "https://github.com/realtimeinnovations/connext-cloud-cli/releases/download"
	DefaultInterval    = 7 * 24 * time.Hour
	NotifyTimeout      = 2 * time.Second

	ConfigLastCheck     = "last_update_check"
	ConfigLatestVersion = "latest_update_version"
	ConfigLatestURL     = "latest_update_url"
	ConfigDisabled      = "update_check_disabled"
)

type Manager struct {
	Config         *config.Manager
	Out            io.Writer
	ErrOut         io.Writer
	HTTPClient     *http.Client
	APIURL         string
	DownloadURL    string
	CheckInterval  time.Duration
	CurrentVersion func() string
	Now            func() time.Time
	Platform       func() (string, string)
	ExecutablePath func() (string, error)
	EvalSymlinks   func(string) (string, error)
	MkdirTemp      func(string, string) (string, error)
	RemoveAll      func(string) error
	Remove         func(string) error
	Rename         func(string, string) error
	Chmod          func(string, os.FileMode) error
	ReadFile       func(string) ([]byte, error)
	WriteFile      func(string, []byte, os.FileMode) error
	MkdirAll       func(string, os.FileMode) error
}

type Options struct {
	CheckOnly bool
	Force     bool
}

type Status struct {
	CurrentVersion  string
	LatestVersion   string
	LatestURL       string
	UpdateAvailable bool
	CheckedRemote   bool
}

type release struct {
	TagName string `json:"tag_name"`
	HTMLURL string `json:"html_url"`
}

type versionParts struct {
	major int
	minor int
	patch int
}

func New(manager *config.Manager, out io.Writer) *Manager {
	return &Manager{
		Config:         manager,
		Out:            out,
		ErrOut:         io.Discard,
		HTTPClient:     &http.Client{Timeout: 30 * time.Second},
		APIURL:         DefaultAPIURL,
		DownloadURL:    DefaultDownloadURL,
		CheckInterval:  DefaultInterval,
		CurrentVersion: buildinfo.Version,
		Now:            time.Now,
		Platform:       func() (string, string) { return runtime.GOOS, runtime.GOARCH },
		ExecutablePath: os.Executable,
		EvalSymlinks:   filepath.EvalSymlinks,
		MkdirTemp:      os.MkdirTemp,
		RemoveAll:      os.RemoveAll,
		Remove:         os.Remove,
		Rename:         os.Rename,
		Chmod:          os.Chmod,
		ReadFile:       os.ReadFile,
		WriteFile:      os.WriteFile,
		MkdirAll:       os.MkdirAll,
	}
}

func (manager *Manager) Run(ctx context.Context, options Options) error {
	out := manager.output()
	status, err := manager.Check(ctx, true)
	if err != nil {
		return err
	}
	if status.LatestVersion == "" {
		_, _ = fmt.Fprintf(out, "Current version: %s\nUnable to determine latest version.\n", status.CurrentVersion)
		return nil
	}
	_, _ = fmt.Fprintf(out, "Current version: %s\nLatest version:  %s\n", status.CurrentVersion, status.LatestVersion)
	if isDevelopmentVersion(status.CurrentVersion) && !options.Force {
		_, _ = fmt.Fprintln(out, "Development builds cannot be updated automatically. Use --force to install the latest release.")
		return nil
	}
	if options.CheckOnly {
		if status.UpdateAvailable {
			_, _ = fmt.Fprintln(out, "Update available. Run 'rticloud update' to install it.")
		} else {
			_, _ = fmt.Fprintln(out, "rticloud is up to date.")
		}
		return nil
	}
	if !status.UpdateAvailable && !options.Force {
		_, _ = fmt.Fprintln(out, "rticloud is up to date.")
		return nil
	}
	if err := manager.install(ctx, status); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Updated rticloud from %s to %s.\n", status.CurrentVersion, status.LatestVersion)
	return nil
}

func (manager *Manager) Check(ctx context.Context, force bool) (Status, error) {
	status := Status{CurrentVersion: manager.currentVersion()}
	if manager.Config == nil {
		return status, errors.New("update checks require configuration storage")
	}
	values, err := manager.Config.GetConfig()
	if err != nil {
		return status, err
	}
	if configBool(values[ConfigDisabled]) {
		return status, nil
	}
	if !force && manager.withinInterval(values[ConfigLastCheck]) {
		status.LatestVersion = values[ConfigLatestVersion]
		status.LatestURL = values[ConfigLatestURL]
		status.UpdateAvailable = isNewerVersion(status.CurrentVersion, status.LatestVersion)
		return status, nil
	}
	latest, err := manager.latestRelease(ctx)
	if err != nil {
		return status, err
	}
	status.LatestVersion = normalizeTag(latest.TagName)
	status.LatestURL = latest.HTMLURL
	status.CheckedRemote = true
	status.UpdateAvailable = isNewerVersion(status.CurrentVersion, status.LatestVersion)
	values[ConfigLastCheck] = manager.now().UTC().Format(time.RFC3339)
	values[ConfigLatestVersion] = status.LatestVersion
	values[ConfigLatestURL] = status.LatestURL
	if err := manager.Config.WriteConfig(values); err != nil {
		return status, err
	}
	return status, nil
}

func (manager *Manager) Notify(ctx context.Context, out io.Writer) {
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, NotifyTimeout)
	defer cancel()
	status, err := manager.Check(ctx, false)
	if err != nil || !status.UpdateAvailable {
		return
	}
	if out == nil {
		out = manager.ErrOut
	}
	if out == nil {
		out = io.Discard
	}
	_, _ = fmt.Fprintf(out, "A newer rticloud version is available: %s. Run 'rticloud update'.\n", status.LatestVersion)
}

func (manager *Manager) latestRelease(ctx context.Context) (release, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(manager.apiURL(), "/")+"/releases/latest", nil)
	if err != nil {
		return release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "rticloud-update-check")
	response, err := manager.httpClient().Do(request)
	if err != nil {
		return release{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return release{}, fmt.Errorf("read latest release response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("release check failed: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var latest release
	if err := json.Unmarshal(body, &latest); err != nil {
		return release{}, err
	}
	if latest.TagName == "" {
		return release{}, errors.New("release response missing tag_name")
	}
	return latest, nil
}

func (manager *Manager) install(ctx context.Context, status Status) error {
	osName, arch := manager.platform()
	archiveName, binaryName, err := artifactNames(osName, arch)
	if err != nil {
		return err
	}
	executable, err := manager.executablePath()
	if err != nil {
		return err
	}
	resolved := executable
	if manager.EvalSymlinks != nil {
		if target, evalErr := manager.EvalSymlinks(executable); evalErr == nil {
			resolved = target
		}
	}
	if isHomebrewPath(executable) || isHomebrewPath(resolved) {
		return errors.New("this rticloud appears to be managed by Homebrew; run 'brew upgrade rticloud' instead")
	}
	if osName == "windows" {
		binaryName += ".exe"
	}
	tmpDir, err := manager.mkdirTemp("", "rticloud-update-*")
	if err != nil {
		return err
	}
	defer manager.removeAll(tmpDir)
	archivePath := filepath.Join(tmpDir, archiveName)
	checksumsPath := filepath.Join(tmpDir, "checksums.txt")
	tag := "v" + normalizeTag(status.LatestVersion)
	if err := manager.download(ctx, downloadURL(manager.downloadURL(), tag, archiveName), archivePath); err != nil {
		return err
	}
	if err := manager.download(ctx, downloadURL(manager.downloadURL(), tag, "checksums.txt"), checksumsPath); err != nil {
		return err
	}
	checksums, err := manager.readFile(checksumsPath)
	if err != nil {
		return err
	}
	archiveBytes, err := manager.readFile(archivePath)
	if err != nil {
		return err
	}
	if err := verifyChecksum(checksums, archiveName, archiveBytes); err != nil {
		return err
	}
	extractedPath := filepath.Join(tmpDir, binaryName)
	if strings.HasSuffix(archiveName, ".zip") {
		err = extractZipBinary(archivePath, binaryName, extractedPath)
	} else {
		err = extractTarGzBinary(archivePath, binaryName, extractedPath)
	}
	if err != nil {
		return err
	}
	if err := manager.chmod(extractedPath, 0o755); err != nil && osName != "windows" {
		return err
	}
	if osName == "windows" {
		return manager.stageWindows(extractedPath, resolved)
	}
	return manager.replaceUnix(extractedPath, resolved)
}

func (manager *Manager) download(ctx context.Context, url string, target string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "rticloud-update")
	response, err := manager.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, err := io.ReadAll(response.Body)
		if err != nil {
			return fmt.Errorf("read download error response: %w", err)
		}
		return fmt.Errorf("download failed for %s: HTTP %d: %s", url, response.StatusCode, strings.TrimSpace(string(body)))
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("download response: %w", copyErr)
	}
	return closeErr
}

func (manager *Manager) replaceUnix(newBinary string, currentBinary string) error {
	staged := filepath.Join(filepath.Dir(currentBinary), "."+filepath.Base(currentBinary)+".update")
	backup := currentBinary + ".bak"
	_ = manager.remove(staged)
	_ = manager.remove(backup)
	if err := copyFile(newBinary, staged, 0o755); err != nil {
		return permissionHint(staged, err)
	}
	if err := manager.chmod(staged, 0o755); err != nil {
		_ = manager.remove(staged)
		return err
	}
	if err := manager.rename(currentBinary, backup); err != nil {
		_ = manager.remove(staged)
		return permissionHint(currentBinary, err)
	}
	if err := manager.rename(staged, currentBinary); err != nil {
		_ = manager.rename(backup, currentBinary)
		_ = manager.remove(staged)
		return err
	}
	_ = manager.remove(backup)
	return nil
}

func copyFile(source string, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (manager *Manager) stageWindows(newBinary string, currentBinary string) error {
	stageDir := filepath.Join(config.DefaultDir(), "updates")
	if err := manager.mkdirAll(stageDir, 0o700); err != nil {
		return err
	}
	stagePath := filepath.Join(stageDir, "rticloud.exe")
	data, err := manager.readFile(newBinary)
	if err != nil {
		return err
	}
	if err := manager.writeFile(stagePath, data, 0o755); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(manager.output(), "Downloaded verified update to %s. Replace %s after this command exits.\n", stagePath, currentBinary)
	return nil
}

func artifactNames(osName string, arch string) (string, string, error) {
	switch osName {
	case "darwin", "linux":
		return fmt.Sprintf("connext-cloud-cli_%s_%s.tar.gz", osName, arch), "rticloud", nil
	case "windows":
		return fmt.Sprintf("connext-cloud-cli_windows_%s.zip", arch), "rticloud", nil
	default:
		return "", "", fmt.Errorf("unsupported platform: %s/%s", osName, arch)
	}
}

func verifyChecksum(checksums []byte, archiveName string, archiveBytes []byte) error {
	expected := ""
	for _, line := range strings.Split(string(checksums), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == archiveName {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum not found for %s", archiveName)
	}
	sum := sha256.Sum256(archiveBytes)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("checksum mismatch for %s", archiveName)
	}
	return nil
}

func extractTarGzBinary(archivePath string, binaryName string, target string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(header.Name) != binaryName {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("%s in archive is not a regular file", binaryName)
		}
		return writeExtractedFile(target, reader)
	}
	return fmt.Errorf("%s not found in archive", binaryName)
}

func extractZipBinary(archivePath string, binaryName string, target string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		if filepath.Base(file.Name) != binaryName {
			continue
		}
		if !file.FileInfo().Mode().IsRegular() {
			return fmt.Errorf("%s in archive is not a regular file", binaryName)
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		defer source.Close()
		return writeExtractedFile(target, source)
	}
	return fmt.Errorf("%s not found in archive", binaryName)
}

func writeExtractedFile(target string, source io.Reader) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, source)
	return err
}

func isNewerVersion(current string, latest string) bool {
	currentParts, ok := parseVersion(current)
	if !ok {
		return false
	}
	latestParts, ok := parseVersion(latest)
	if !ok {
		return false
	}
	if latestParts.major != currentParts.major {
		return latestParts.major > currentParts.major
	}
	if latestParts.minor != currentParts.minor {
		return latestParts.minor > currentParts.minor
	}
	return latestParts.patch > currentParts.patch
}

func parseVersion(value string) (versionParts, bool) {
	value = normalizeTag(value)
	if value == "" || value == "dev" || strings.ContainsAny(value, "+-") {
		return versionParts{}, false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return versionParts{}, false
	}
	numbers := make([]int, 3)
	for idx, part := range parts {
		if part == "" {
			return versionParts{}, false
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return versionParts{}, false
		}
		numbers[idx] = number
	}
	return versionParts{major: numbers[0], minor: numbers[1], patch: numbers[2]}, true
}

func normalizeTag(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func isDevelopmentVersion(value string) bool {
	_, ok := parseVersion(value)
	return !ok
}

func configBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isHomebrewPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	lower := strings.ToLower(clean)
	return strings.Contains(lower, "/homebrew/cellar/rticloud/") || strings.Contains(lower, "/cellar/rticloud/")
}

func permissionHint(path string, err error) error {
	if os.IsPermission(err) {
		return fmt.Errorf("cannot write to %s; re-run the installer with a writable INSTALL_DIR or with appropriate privileges", filepath.Dir(path))
	}
	return err
}

func downloadURL(base string, tag string, name string) string {
	return strings.TrimRight(base, "/") + "/" + tag + "/" + name
}

func (manager *Manager) withinInterval(value string) bool {
	if value == "" {
		return false
	}
	checkedAt, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	interval := manager.CheckInterval
	if interval <= 0 {
		interval = DefaultInterval
	}
	elapsed := manager.now().Sub(checkedAt)
	return elapsed >= 0 && elapsed < interval
}

func (manager *Manager) currentVersion() string {
	if manager.CurrentVersion != nil {
		return normalizeTag(manager.CurrentVersion())
	}
	return normalizeTag(buildinfo.Version())
}

func (manager *Manager) now() time.Time {
	if manager.Now != nil {
		return manager.Now()
	}
	return time.Now()
}

func (manager *Manager) platform() (string, string) {
	if manager.Platform != nil {
		return manager.Platform()
	}
	return runtime.GOOS, runtime.GOARCH
}

func (manager *Manager) executablePath() (string, error) {
	if manager.ExecutablePath != nil {
		return manager.ExecutablePath()
	}
	return os.Executable()
}

func (manager *Manager) httpClient() *http.Client {
	if manager.HTTPClient != nil {
		return manager.HTTPClient
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (manager *Manager) apiURL() string {
	if manager.APIURL != "" {
		return manager.APIURL
	}
	return DefaultAPIURL
}

func (manager *Manager) downloadURL() string {
	if manager.DownloadURL != "" {
		return manager.DownloadURL
	}
	return DefaultDownloadURL
}

func (manager *Manager) output() io.Writer {
	if manager.Out != nil {
		return manager.Out
	}
	return io.Discard
}

func (manager *Manager) mkdirTemp(dir string, pattern string) (string, error) {
	if manager.MkdirTemp != nil {
		return manager.MkdirTemp(dir, pattern)
	}
	return os.MkdirTemp(dir, pattern)
}

func (manager *Manager) removeAll(path string) error {
	if manager.RemoveAll != nil {
		return manager.RemoveAll(path)
	}
	return os.RemoveAll(path)
}

func (manager *Manager) remove(path string) error {
	if manager.Remove != nil {
		return manager.Remove(path)
	}
	return os.Remove(path)
}

func (manager *Manager) rename(oldPath string, newPath string) error {
	if manager.Rename != nil {
		return manager.Rename(oldPath, newPath)
	}
	return os.Rename(oldPath, newPath)
}

func (manager *Manager) chmod(path string, mode os.FileMode) error {
	if manager.Chmod != nil {
		return manager.Chmod(path, mode)
	}
	return os.Chmod(path, mode)
}

func (manager *Manager) readFile(path string) ([]byte, error) {
	if manager.ReadFile != nil {
		return manager.ReadFile(path)
	}
	return os.ReadFile(path)
}

func (manager *Manager) writeFile(path string, data []byte, mode os.FileMode) error {
	if manager.WriteFile != nil {
		return manager.WriteFile(path, data, mode)
	}
	return os.WriteFile(path, data, mode)
}

func (manager *Manager) mkdirAll(path string, mode os.FileMode) error {
	if manager.MkdirAll != nil {
		return manager.MkdirAll(path, mode)
	}
	return os.MkdirAll(path, mode)
}
