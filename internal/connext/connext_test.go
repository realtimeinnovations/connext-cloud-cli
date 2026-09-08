// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHasCloudExtrasDetectsPackageMetadata(t *testing.T) {
	patched := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiddsspy")
	writeVersions(t, patched, `
<rti>
  <utils-bin>
    <installation>
      <architecture>arm64Darwin</architecture>
      <version>7.7.0_RTI_ER_723</version>
      <installer_name>rti_connext_dds-7.7.0_RTI_ER_723-arm64Darwin-cloud-extras.rtipkg</installer_name>
    </installation>
  </utils-bin>
</rti>`)
	base := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiddsspy")
	writeVersions(t, base, `
<rti>
  <utils-bin>
    <installation>
      <architecture>arm64Darwin23clang16.0</architecture>
      <version>7.7.0</version>
      <installer_name>rti_connext_dds-7.7.0-lm-target-arm64Darwin23clang16.0.rtipkg</installer_name>
    </installation>
  </utils-bin>
</rti>`)

	if !HasCloudExtras(Install{Path: patched, Version: "7.7.0"}) {
		t.Fatal("expected cloud extras marker")
	}
	if !HasEnhancedDDSSpy(Install{Path: patched, Version: "7.7.0"}) {
		t.Fatal("expected enhanced rtiddsspy")
	}
	if HasCloudExtras(Install{Path: base, Version: "7.7.0"}) {
		t.Fatal("did not expect base install to have cloud extras")
	}
	if HasEnhancedDDSSpy(Install{Path: base, Version: "7.7.0"}) {
		t.Fatal("did not expect enhanced rtiddsspy")
	}
	patchRelease := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiddsspy")
	writeVersions(t, patchRelease, `
<rti>
  <utils-bin>
    <installation>
      <architecture>arm64Darwin23clang16.0</architecture>
      <version>7.7.0.1</version>
      <installer_name>rti_connext_dds-7.7.0.1-lm-target-arm64Darwin23clang16.0.rtipkg</installer_name>
    </installation>
  </utils-bin>
</rti>`)
	if !HasEnhancedDDSSpy(Install{Path: patchRelease, Version: "7.7.0"}) {
		t.Fatal("expected 7.7.0.x rtiddsspy to be treated as enhanced")
	}
	newer := createConnextInstall(t, t.TempDir(), "7.7.1", "rtiddsspy")
	writeVersions(t, newer, `<rti></rti>`)
	if !HasEnhancedDDSSpy(Install{Path: newer, Version: "7.7.1"}) {
		t.Fatal("expected 7.7.1+ rtiddsspy to be treated as enhanced")
	}
}

func TestLicenseManagedInstallDetectionAndLicenseAvailability(t *testing.T) {
	install := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiddsspy")
	writeVersions(t, install, `
<rti>
  <host>
    <installation_type>LM</installation_type>
  </host>
</rti>`)
	connextInstall := Install{Path: install, Version: "7.7.0"}
	if !IsLicenseManaged(connextInstall) {
		t.Fatal("expected LM installation")
	}
	if HasLicenseAvailable(connextInstall) {
		t.Fatal("did not expect license before writing one")
	}
	if err := WriteLicenseFile(connextInstall, []byte("license-body")); err != nil {
		t.Fatal(err)
	}
	if !HasLicenseAvailable(connextInstall) {
		t.Fatal("expected license file in installation directory")
	}
	nonLM := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiddsspy")
	writeVersions(t, nonLM, `
<rti>
  <host>
    <installation_type>HOST</installation_type>
  </host>
</rti>`)
	if IsLicenseManaged(Install{Path: nonLM, Version: "7.7.0"}) {
		t.Fatal("did not expect non-LM installation")
	}
}

func TestHasLicenseAvailableUsesRTILicenseFile(t *testing.T) {
	install := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiddsspy")
	licensePath := filepath.Join(t.TempDir(), "rti_license.dat")
	if err := os.WriteFile(licensePath, []byte("license-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTI_LICENSE_FILE", licensePath)
	if !HasLicenseAvailable(Install{Path: install, Version: "7.7.0"}) {
		t.Fatal("expected RTI_LICENSE_FILE to satisfy license check")
	}
}

func TestCloudExtrasPackageURLSelectsPlatformPackage(t *testing.T) {
	previousPlatform := Platform
	t.Cleanup(func() { Platform = previousPlatform })
	tests := []struct {
		goos string
		arch string
		want string
	}{
		{"linux", "arm64", "armv8Linux-cloud-extras.rtipkg"},
		{"darwin", "arm64", "arm64Darwin-cloud-extras.rtipkg"},
		{"linux", "amd64", "x64Linux-cloud-extras.rtipkg"},
		{"windows", "amd64", "x64Win64-cloud-extras.rtipkg"},
	}
	for _, test := range tests {
		Platform = func() (string, string) { return test.goos, test.arch }
		url, err := cloudExtrasPackageURL()
		if err != nil {
			t.Fatalf("unexpected error for %s/%s: %v", test.goos, test.arch, err)
		}
		if !strings.HasSuffix(url, test.want) {
			t.Fatalf("url for %s/%s = %s, want suffix %s", test.goos, test.arch, url, test.want)
		}
	}
}

func TestEnsureCollectorServiceLiteInstallsCloudExtras(t *testing.T) {
	tmpDir := t.TempDir()
	install := createConnextInstall(t, tmpDir, "7.7.0", "rtiroutingservice", "rtipkginstall")
	writeVersions(t, install, `<rti></rti>`)

	previousGetwd := CurrentWorkDir
	previousPlatform := Platform
	previousHTTPGet := HTTPGet
	previousInstaller := PackageInstaller
	CurrentWorkDir = func() (string, error) { return tmpDir, nil }
	Platform = func() (string, string) { return "darwin", "arm64" }
	HTTPGet = func(url string) (*http.Response, error) {
		if !strings.HasSuffix(url, "arm64Darwin-cloud-extras.rtipkg") {
			t.Fatalf("unexpected url: %s", url)
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader("package"))}, nil
	}
	PackageInstaller = func(install Install, packagePath string, out io.Writer) error {
		if !strings.HasSuffix(packagePath, "arm64Darwin-cloud-extras.rtipkg") {
			t.Fatalf("unexpected package path: %s", packagePath)
		}
		if err := os.WriteFile(filepath.Join(install.Path, "bin", "rticollectorservicelite"), []byte(""), 0o755); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	t.Cleanup(func() {
		CurrentWorkDir = previousGetwd
		Platform = previousPlatform
		HTTPGet = previousHTTPGet
		PackageInstaller = previousInstaller
	})

	var out bytes.Buffer
	selected := false
	err := EnsureCollectorServiceLite(Install{Path: install, Version: "7.7.0"}, func(message string, choices []string) (string, error) {
		selected = true
		if !strings.Contains(message, "RTI Collector Service Lite") || len(choices) != 2 || choices[0] != InstallConnextCloudExtrasLabel {
			t.Fatalf("unexpected prompt: %q %#v", message, choices)
		}
		return InstallConnextCloudExtrasLabel, nil
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !selected {
		t.Fatal("expected install prompt")
	}
	if !strings.Contains(out.String(), "Connext Cloud Extras package installed") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestEnsureEnhancedDDSSpySkipsPatchForConnext770PatchRelease(t *testing.T) {
	install := createConnextInstall(t, t.TempDir(), "7.7.0", "rtiddsspy")
	writeVersions(t, install, `
<rti>
  <core>
    <installation>
      <version>7.7.0.1</version>
    </installation>
  </core>
</rti>`)

	err := EnsureEnhancedDDSSpy(Install{Path: install, Version: "7.7.0"}, func(message string, choices []string) (string, error) {
		t.Fatalf("did not expect patch prompt for 7.7.0.x install: %s %#v", message, choices)
		return "", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureEnhancedDDSSpySkipsPatchForConnext771OrNewer(t *testing.T) {
	install := createConnextInstall(t, t.TempDir(), "7.7.1", "rtiddsspy")
	writeVersions(t, install, `<rti></rti>`)

	err := EnsureEnhancedDDSSpy(Install{Path: install, Version: "7.7.1"}, func(message string, choices []string) (string, error) {
		t.Fatalf("did not expect patch prompt for 7.7.1+ install: %s %#v", message, choices)
		return "", nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureCloudExtrasRejectsUnsupportedVersions(t *testing.T) {
	older := Install{Path: "/tmp/rti_connext_dds-7.6.0", Version: "7.6.0"}
	if err := EnsureCollectorServiceLite(older, nil, nil); err == nil || !strings.Contains(err.Error(), "requires Connext Pro 7.7.0") {
		t.Fatalf("unexpected old-version error: %v", err)
	}
	newer := Install{Path: "/tmp/rti_connext_dds-7.7.1", Version: "7.7.1"}
	if err := EnsureCollectorServiceLite(newer, nil, nil); err == nil || !strings.Contains(err.Error(), "unexpected for Connext Pro 7.7.1") {
		t.Fatalf("unexpected new-version error: %v", err)
	}
}

func createConnextInstall(t *testing.T, root string, version string, executables ...string) string {
	t.Helper()
	install := filepath.Join(root, "rti_connext_dds-"+version)
	binDir := filepath.Join(install, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, executable := range executables {
		if err := os.WriteFile(filepath.Join(binDir, executable), []byte(""), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return install
}

func writeVersions(t *testing.T, install string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(install, "rti_versions.xml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPackageInstallerUsesSelectedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX test executable")
	}
	directory := createConnextInstall(t, t.TempDir(), "7.7.0", "rtipkginstall")
	script := `#!/bin/sh
[ "$(cd "$NDDSHOME" && pwd -P)" = "$(pwd -P)" ] || exit 21
[ "$(cd "$CONNEXTDDS_DIR" && pwd -P)" = "$(pwd -P)" ] || exit 22
`
	if err := os.WriteFile(filepath.Join(directory, "bin", "rtipkginstall"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NDDSHOME", "rejected-external-install")
	t.Setenv("CONNEXTDDS_DIR", "another-install")
	if err := runPackageInstaller(Install{Path: directory}, "test.rtipkg", io.Discard); err != nil {
		t.Fatal(err)
	}
}
