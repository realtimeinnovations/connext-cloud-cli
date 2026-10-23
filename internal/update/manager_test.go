package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/config"
)

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "patch newer", current: "1.2.3", latest: "1.2.4", want: true},
		{name: "minor newer", current: "1.2.3", latest: "1.3.0", want: true},
		{name: "major newer", current: "1.2.3", latest: "2.0.0", want: true},
		{name: "same", current: "1.2.3", latest: "v1.2.3"},
		{name: "older", current: "1.2.3", latest: "1.2.2"},
		{name: "dev current", current: "dev", latest: "1.2.3"},
		{name: "prerelease latest", current: "1.2.3", latest: "1.2.4-beta.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isNewerVersion(test.current, test.latest); got != test.want {
				t.Fatalf("isNewerVersion(%q, %q) = %v, want %v", test.current, test.latest, got, test.want)
			}
		})
	}
}

func TestArtifactNames(t *testing.T) {
	tests := []struct {
		osName      string
		arch        string
		wantArchive string
		wantBinary  string
	}{
		{osName: "darwin", arch: "arm64", wantArchive: "connext-cloud-cli_darwin_arm64.tar.gz", wantBinary: "rticloud"},
		{osName: "linux", arch: "amd64", wantArchive: "connext-cloud-cli_linux_amd64.tar.gz", wantBinary: "rticloud"},
	}
	for _, test := range tests {
		t.Run(test.wantArchive, func(t *testing.T) {
			archiveName, binaryName, err := artifactNames(test.osName, test.arch)
			if err != nil {
				t.Fatal(err)
			}
			if archiveName != test.wantArchive || binaryName != test.wantBinary {
				t.Fatalf("artifactNames() = %q, %q", archiveName, binaryName)
			}
		})
	}
}

func TestDefaultIntervalIsOneWeek(t *testing.T) {
	if DefaultInterval != 7*24*time.Hour {
		t.Fatalf("DefaultInterval = %s, want %s", DefaultInterval, 7*24*time.Hour)
	}
}

func TestCheckFetchesLatestAndCaches(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != "/releases/latest" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		_, _ = fmt.Fprint(writer, `{"tag_name":"v1.2.4","html_url":"https://example.test/release"}`)
	}))
	defer server.Close()

	manager := newTestManager(t, server.URL)
	manager.CurrentVersion = func() string { return "1.2.3" }
	manager.Now = func() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }

	status, err := manager.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || !status.CheckedRemote || status.LatestVersion != "1.2.4" {
		t.Fatalf("unexpected status: %#v", status)
	}

	status, err = manager.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if status.CheckedRemote {
		t.Fatalf("expected cached check, got remote: %#v", status)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1", calls)
	}
}

func TestWithinIntervalTreatsFutureTimestampAsStale(t *testing.T) {
	manager := newTestManager(t, "http://127.0.0.1")
	future := manager.now().Add(time.Hour).Format(time.RFC3339)
	if manager.withinInterval(future) {
		t.Fatal("future timestamp should be stale")
	}
}

func TestCheckDisabledSkipsNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Fatal("unexpected network call")
	}))
	defer server.Close()

	manager := newTestManager(t, server.URL)
	if err := manager.Config.WriteConfig(map[string]string{ConfigDisabled: "true"}); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Check(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if status.LatestVersion != "" || status.UpdateAvailable {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestCheckForcedIgnoresDisabledConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/releases/latest" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		_, _ = fmt.Fprint(writer, `{"tag_name":"v1.2.4","html_url":"https://example.test/release"}`)
	}))
	defer server.Close()

	manager := newTestManager(t, server.URL)
	manager.CurrentVersion = func() string { return "1.2.3" }
	if err := manager.Config.WriteConfig(map[string]string{ConfigDisabled: "true"}); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !status.CheckedRemote || !status.UpdateAvailable || status.LatestVersion != "1.2.4" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestCheckReturnsReleaseBodyReadError(t *testing.T) {
	manager := newTestManager(t, "http://127.0.0.1")
	manager.HTTPClient = roundTripClient(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{})}, nil
	})

	_, err := manager.Check(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "read latest release response") {
		t.Fatalf("expected release body read error, got %v", err)
	}
}

func TestVerifyChecksumRejectsMismatch(t *testing.T) {
	archiveName := "connext-cloud-cli_linux_amd64.tar.gz"
	checksums := []byte("0000000000000000000000000000000000000000000000000000000000000000  " + archiveName + "\n")
	archivePath := filepath.Join(t.TempDir(), archiveName)
	if err := os.WriteFile(archivePath, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(checksums, archiveName, archivePath); err == nil {
		t.Fatal("expected checksum mismatch")
	}
}

func TestDownloadStreamsResponseToFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("download-body"))
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "download.bin")
	manager := newTestManager(t, server.URL)
	if err := manager.download(context.Background(), server.URL+"/archive", target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "download-body" {
		t.Fatalf("downloaded data = %q", string(data))
	}
}

func TestDownloadReturnsBodyReadError(t *testing.T) {
	manager := newTestManager(t, "http://127.0.0.1")
	manager.HTTPClient = roundTripClient(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(errReader{})}, nil
	})

	err := manager.download(context.Background(), "http://127.0.0.1/archive", filepath.Join(t.TempDir(), "archive"))
	if err == nil || !strings.Contains(err.Error(), "download response") {
		t.Fatalf("expected download response read error, got %v", err)
	}
}

func TestExtractTarGzBinary(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "archive.tar.gz")
	writeTarGz(t, archivePath, "rticloud", []byte("new-binary"))
	target := filepath.Join(tmpDir, "rticloud")
	if err := extractTarGzBinary(archivePath, "rticloud", target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("extracted data = %q", string(data))
	}
}

func TestExtractTarGzRejectsNonRegularBinary(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "archive.tar.gz")
	writeTarGzEntry(t, archivePath, &tar.Header{Name: "rticloud", Typeflag: tar.TypeSymlink, Linkname: "target"}, nil)
	err := extractTarGzBinary(archivePath, "rticloud", filepath.Join(tmpDir, "rticloud"))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular tar entry error, got %v", err)
	}
}

func TestRunInstallsVerifiedUnixArchive(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "rticloud")
	if err := os.WriteFile(binaryPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archiveName := "connext-cloud-cli_linux_amd64.tar.gz"
	archiveBytes := tarGzBytes(t, "rticloud", []byte("new-binary"))
	checksums := checksumsFor(archiveName, archiveBytes)
	server := updateServer(t, map[string][]byte{
		"/releases/latest":       []byte(`{"tag_name":"v1.2.4","html_url":"https://example.test/release"}`),
		"/v1.2.4/" + archiveName: archiveBytes,
		"/v1.2.4/checksums.txt":  []byte(checksums),
	})
	defer server.Close()

	var out bytes.Buffer
	manager := newTestManager(t, server.URL)
	manager.DownloadURL = server.URL
	manager.Out = &out
	manager.CurrentVersion = func() string { return "1.2.3" }
	manager.Platform = func() (string, string) { return "linux", "amd64" }
	manager.ExecutablePath = func() (string, error) { return binaryPath, nil }
	manager.EvalSymlinks = func(path string) (string, error) { return path, nil }

	if err := manager.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("updated binary = %q", string(data))
	}
	if !strings.Contains(out.String(), "Updated rticloud from 1.2.3 to 1.2.4") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRunPreservesInstalledBinary(t *testing.T) {
	errorsByScenario := map[string]string{
		"checksum-mismatch":    "checksum mismatch",
		"missing-checksum":     "checksum not found",
		"invalid-archive":      "gzip: invalid header",
		"missing-binary":       "rticloud not found in archive",
		"nonregular-binary":    "not a regular file",
		"release-http-error":   "release check failed: HTTP 503",
		"archive-http-error":   "HTTP 503",
		"checksum-http-error":  "HTTP 503",
		"interrupted-download": "unexpected EOF",
		"rename-failure":       "replacement rename failed",
	}
	for _, scenario := range []string{"checksum-mismatch", "missing-checksum", "invalid-archive", "missing-binary", "nonregular-binary", "release-http-error", "archive-http-error", "checksum-http-error", "interrupted-download", "rename-failure", "check-only", "up-to-date"} {
		t.Run(scenario, func(t *testing.T) {
			installDir := t.TempDir()
			binaryPath := filepath.Join(installDir, "rticloud")
			if err := os.WriteFile(binaryPath, []byte("original binary"), 0751); err != nil {
				t.Fatal(err)
			}
			originalInfo, err := os.Stat(binaryPath)
			if err != nil {
				t.Fatal(err)
			}
			const archiveName = "connext-cloud-cli_linux_amd64.tar.gz"
			archive := tarGzBytes(t, "rticloud", []byte("replacement binary"))
			switch scenario {
			case "invalid-archive":
				archive = []byte("not a gzip archive")
			case "missing-binary":
				archive = tarGzBytes(t, "other", []byte("not the executable"))
			case "nonregular-binary":
				path := filepath.Join(t.TempDir(), "fixture.tar.gz")
				writeTarGzEntry(t, path, &tar.Header{Name: "rticloud", Typeflag: tar.TypeSymlink, Linkname: "other"}, nil)
				var err error
				archive, err = os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			checksums := checksumsFor(archiveName, archive)
			if scenario == "checksum-mismatch" {
				checksums = checksumsFor(archiveName, []byte("different bytes"))
			}
			if scenario == "missing-checksum" {
				checksums = checksumsFor("other.tar.gz", archive)
			}
			var downloads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					http.Error(w, "unexpected method", 405)
					return
				}
				switch r.URL.Path {
				case "/releases/latest":
					if scenario == "release-http-error" {
						http.Error(w, "release unavailable", 503)
						return
					}
					version := "v1.2.4"
					if scenario == "up-to-date" {
						version = "v1.2.3"
					}
					fmt.Fprintf(w, `{"tag_name":%q}`, version)
				case "/v1.2.4/" + archiveName:
					downloads.Add(1)
					if scenario == "archive-http-error" {
						http.Error(w, "archive unavailable", 503)
						return
					}
					if scenario == "interrupted-download" {
						w.Header().Set("Content-Length", fmt.Sprint(len(archive)+100))
						_, _ = w.Write(archive[:len(archive)/2])
						return
					}
					_, _ = w.Write(archive)
				case "/v1.2.4/checksums.txt":
					downloads.Add(1)
					if scenario == "checksum-http-error" {
						http.Error(w, "checksums unavailable", 503)
						return
					}
					_, _ = io.WriteString(w, checksums)
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			var out bytes.Buffer
			manager := newTestManager(t, server.URL)
			manager.DownloadURL = server.URL
			manager.Out = &out
			manager.CurrentVersion = func() string { return "1.2.3" }
			manager.Platform = func() (string, string) { return "linux", "amd64" }
			manager.ExecutablePath = func() (string, error) { return binaryPath, nil }
			temporaryRoot := t.TempDir()
			manager.MkdirTemp = func(_ string, pattern string) (string, error) { return os.MkdirTemp(temporaryRoot, pattern) }
			if scenario == "rename-failure" {
				manager.Rename = func(oldPath, newPath string) error {
					if filepath.Base(oldPath) == ".rticloud.update" {
						return fmt.Errorf("replacement rename failed")
					}
					return os.Rename(oldPath, newPath)
				}
			}
			err = manager.Run(context.Background(), Options{CheckOnly: scenario == "check-only"})
			wantError := scenario != "check-only" && scenario != "up-to-date"
			if (err != nil) != wantError {
				t.Fatalf("err=%v wantError=%v output=%s", err, wantError, out.String())
			}
			if wantError && !strings.Contains(err.Error(), errorsByScenario[scenario]) {
				t.Fatalf("wrong failure: %v; want %q", err, errorsByScenario[scenario])
			}
			data, readErr := os.ReadFile(binaryPath)
			if readErr != nil || string(data) != "original binary" {
				t.Fatalf("original binary changed: %q %v", data, readErr)
			}
			info, statErr := os.Stat(binaryPath)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm() != originalInfo.Mode().Perm() {
				t.Fatalf("binary mode changed: %v", info.Mode())
			}
			for _, path := range []string{filepath.Join(installDir, ".rticloud.update"), binaryPath + ".bak"} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("replacement debris %s: %v", path, err)
				}
			}
			leftovers, err := os.ReadDir(temporaryRoot)
			if err != nil || len(leftovers) != 0 {
				t.Fatalf("download directory not cleaned: %v %v", leftovers, err)
			}
			if strings.Contains(out.String(), "Updated rticloud") {
				t.Fatalf("failure/check reported success: %s", out.String())
			}
			wantDownloads := int32(2)
			switch scenario {
			case "release-http-error", "check-only", "up-to-date":
				wantDownloads = 0
			case "archive-http-error", "interrupted-download":
				wantDownloads = 1
			}
			if downloads.Load() != wantDownloads {
				t.Fatalf("downloads=%d, want %d", downloads.Load(), wantDownloads)
			}
			if scenario == "check-only" && !strings.Contains(out.String(), "Update available") {
				t.Fatalf("missing update hint: %s", out.String())
			}
			if scenario == "up-to-date" && !strings.Contains(out.String(), "rticloud is up to date.") {
				t.Fatalf("missing version result: %s", out.String())
			}
		})
	}
}

func TestRunShowsWindowsInstallHint(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.URL.Path != "/releases/latest" {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		_, _ = fmt.Fprint(writer, `{"tag_name":"v1.2.4","html_url":"https://example.test/release"}`)
	}))
	defer server.Close()

	var out bytes.Buffer
	manager := newTestManager(t, server.URL)
	manager.Out = &out
	manager.CurrentVersion = func() string { return "1.2.3" }
	manager.Platform = func() (string, string) { return "windows", "amd64" }
	manager.ExecutablePath = func() (string, error) {
		t.Fatal("windows update should not inspect or replace the running executable")
		return "", nil
	}

	if err := manager.Run(context.Background(), Options{}); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	for _, want := range []string{"Current version: 1.2.3", "Latest version:  1.2.4", WindowsInstallHint} {
		if !strings.Contains(output, want) {
			t.Fatalf("missing %q in output: %s", want, output)
		}
	}
	if strings.Contains(output, "Updated rticloud") {
		t.Fatalf("windows update should not report direct replacement: %s", output)
	}
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1", calls)
	}
}

func TestRunReportsDevelopmentBuild(t *testing.T) {
	tests := []struct {
		name    string
		options Options
	}{
		{name: "check", options: Options{CheckOnly: true}},
		{name: "install", options: Options{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			manager := newTestManager(t, "http://127.0.0.1")
			manager.Out = &out
			manager.CurrentVersion = func() string { return "dev" }
			manager.HTTPClient = roundTripClient(func(request *http.Request) (*http.Response, error) {
				return stringResponse(http.StatusOK, `{"tag_name":"v1.2.4"}`), nil
			})

			if err := manager.Run(context.Background(), test.options); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "Development builds cannot be updated automatically") {
				t.Fatalf("missing development-build guidance: %s", out.String())
			}
			if strings.Contains(out.String(), "rticloud is up to date") {
				t.Fatalf("development build should not be reported up to date: %s", out.String())
			}
		})
	}
}

func TestReplaceUnixStagesBinaryInInstallDirectory(t *testing.T) {
	installDir := t.TempDir()
	otherDir := t.TempDir()
	currentBinary := filepath.Join(installDir, "rticloud")
	newBinary := filepath.Join(otherDir, "rticloud")
	if err := os.WriteFile(currentBinary, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newBinary, []byte("new-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	manager := New(nil, io.Discard)
	manager.Rename = func(oldPath string, newPath string) error {
		if filepath.Dir(oldPath) != filepath.Dir(newPath) {
			t.Fatalf("cross-directory rename attempted: %s -> %s", oldPath, newPath)
		}
		return os.Rename(oldPath, newPath)
	}
	if err := manager.replaceUnix(newBinary, currentBinary); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("current binary = %q", string(data))
	}
	if _, err := os.Stat(filepath.Join(installDir, ".rticloud.update")); !os.IsNotExist(err) {
		t.Fatalf("staged file should be removed, stat error: %v", err)
	}
}

func TestRunDetectsHomebrewInstall(t *testing.T) {
	manager := newTestManager(t, "http://127.0.0.1")
	manager.CurrentVersion = func() string { return "1.2.3" }
	manager.Platform = func() (string, string) { return "darwin", "arm64" }
	manager.ExecutablePath = func() (string, error) { return "/opt/homebrew/Cellar/rticloud/1.2.3/bin/rticloud", nil }
	manager.EvalSymlinks = func(path string) (string, error) { return path, nil }
	manager.HTTPClient = roundTripClient(func(request *http.Request) (*http.Response, error) {
		return stringResponse(http.StatusOK, `{"tag_name":"v1.2.4"}`), nil
	})

	err := manager.Run(context.Background(), Options{})
	if err == nil || !strings.Contains(err.Error(), "brew upgrade rticloud") {
		t.Fatalf("expected Homebrew guidance, got %v", err)
	}
}

func newTestManager(t *testing.T, apiURL string) *Manager {
	t.Helper()
	manager := New(config.New(filepath.Join(t.TempDir(), "config.json")), io.Discard)
	manager.APIURL = apiURL
	manager.Now = func() time.Time { return time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC) }
	return manager
}

func updateServer(t *testing.T, responses map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, ok := responses[request.URL.Path]
		if !ok {
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
		_, _ = writer.Write(body)
	}))
}

func tarGzBytes(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func writeTarGz(t *testing.T, path string, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, tarGzBytes(t, name, data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTarGzEntry(t *testing.T, path string, header *tar.Header, data []byte) {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	if header.Size == 0 && len(data) > 0 {
		header.Size = int64(len(data))
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if len(data) > 0 {
		if _, err := tarWriter.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func checksumsFor(name string, data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func roundTripClient(fn roundTripFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func stringResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("read failed")
}
