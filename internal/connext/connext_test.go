package connext

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverInstallIncludesNonStandardDirWarningWhenNotFound(t *testing.T) {
	previousPatterns := append([]string(nil), InstallPatterns...)
	InstallPatterns = nil
	t.Cleanup(func() { InstallPatterns = previousPatterns })

	_, err := DiscoverInstallWithPrompt(map[string]string{}, false, nil, nil, DiscoveryOptions{MinVersion: "7.7.0", ExecutableName: "rtiddsspy", CommandName: "spy"})
	if err == nil || !strings.Contains(err.Error(), nonStandardDirWarning()) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDiscoverInstallWithPromptAllowsEnteringCustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(filepath.Join(install, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	previousPatterns := append([]string(nil), InstallPatterns...)
	InstallPatterns = nil
	t.Cleanup(func() { InstallPatterns = previousPatterns })

	result, err := DiscoverInstallWithPrompt(map[string]string{}, true,
		func(message string, choices []string) (string, error) { return EnterConnextPathLabel, nil },
		func(message string) (string, error) { return install, nil },
		DiscoveryOptions{MinVersion: "7.3.0", ExecutableName: "rtiroutingservice", CommandName: "gateway"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != install {
		t.Fatalf("unexpected install: %#v", result)
	}
}

func TestDiscoverInstallWithPromptAppendsCustomPathAfterDetectedInstalls(t *testing.T) {
	tmpDir := t.TempDir()
	defaultInstall := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	customInstall := filepath.Join(tmpDir, "custom", "rti_connext_dds-7.8.0")
	for _, install := range []string{defaultInstall, customInstall} {
		if err := os.MkdirAll(filepath.Join(install, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(install, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	previousPatterns := append([]string(nil), InstallPatterns...)
	InstallPatterns = []string{filepath.Join(tmpDir, "rti_connext_dds-*")}
	t.Cleanup(func() { InstallPatterns = previousPatterns })

	var gotChoices []string
	result, err := DiscoverInstallWithPrompt(map[string]string{}, true,
		func(message string, choices []string) (string, error) {
			gotChoices = append([]string(nil), choices...)
			return EnterConnextPathLabel, nil
		},
		func(message string) (string, error) { return customInstall, nil },
		DiscoveryOptions{MinVersion: "7.3.0", ExecutableName: "rtiroutingservice", CommandName: "gateway"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotChoices) != 2 || gotChoices[0] != defaultInstall || gotChoices[1] != EnterConnextPathLabel {
		t.Fatalf("unexpected choices: %#v", gotChoices)
	}
	if result.Path != customInstall {
		t.Fatalf("unexpected install: %#v", result)
	}
}

func TestDiscoverInstallWithPromptDownloadsInstaller(t *testing.T) {
	tmpDir := t.TempDir()
	previousPatterns := append([]string(nil), InstallPatterns...)
	previousGetwd := CurrentWorkDir
	previousPlatform := Platform
	previousHTTPGet := HTTPGet
	InstallPatterns = nil
	CurrentWorkDir = func() (string, error) { return tmpDir, nil }
	Platform = func() (string, string) { return "linux", "amd64" }
	HTTPGet = func(url string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("installer"))}, nil
	}
	t.Cleanup(func() {
		InstallPatterns = previousPatterns
		CurrentWorkDir = previousGetwd
		Platform = previousPlatform
		HTTPGet = previousHTTPGet
	})

	_, err := DiscoverInstallWithPrompt(map[string]string{}, true,
		func(message string, choices []string) (string, error) { return DownloadConnextLabel, nil },
		func(message string) (string, error) { return "", nil },
		DiscoveryOptions{MinVersion: "7.3.0", ExecutableName: "rtiroutingservice", CommandName: "gateway"},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), filepath.Join(tmpDir, "rti_connext_dds-7.7.0-lm-x64Linux4gcc8.5.0.run")) {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "Downloaded Connext Professional installer") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpDir, "rti_connext_dds-7.7.0-lm-x64Linux4gcc8.5.0.run")); statErr != nil {
		t.Fatalf("expected downloaded installer: %v", statErr)
	}
}
