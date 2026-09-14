// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInstallerCacheReuseReplacementAndCancellation(t *testing.T) {
	managedTestEnvironment(t)
	body := "verified installer"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
	calls := 0
	client := &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	url := "https://example.invalid/installer.run"
	path, err := cachedInstaller(context.Background(), url, digest, client, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cachedInstaller(context.Background(), url, digest, client, io.Discard); err != nil || calls != 1 {
		t.Fatalf("cache reuse: calls=%d err=%v", calls, err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := cachedInstaller(context.Background(), url, digest, client, io.Discard); err != nil || calls != 2 {
		t.Fatalf("cache repair: calls=%d err=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cachedInstaller(ctx, url, digest, client, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := cachedInstaller(context.Background(), url, "bad hash", client, io.Discard); err == nil {
		t.Fatal("accepted corrupt download")
	}
	parts, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.part"))
	if len(parts) != 0 {
		t.Fatal(parts)
	}
	data, _ := os.ReadFile(path)
	if string(data) != body {
		t.Fatal("failed download replaced good cache")
	}
}

func TestDownloadCancellationCleansPartial(t *testing.T) {
	managedTestEnvironment(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, r.Context().Err()
	})}
	_, err := cachedInstaller(ctx, "https://example.invalid/cancel.run", "unused", client, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	home, _ := UserHomeDir()
	parts, _ := filepath.Glob(filepath.Join(home, "Downloads", "*.part"))
	if len(parts) != 0 {
		t.Fatal(parts)
	}
}

func TestManagedLicensePersistsRepairsAndOverridesEnvironment(t *testing.T) {
	root := managedTestEnvironment(t)
	t.Setenv("NDDSHOME", "")
	t.Setenv("RTI_LICENSE_FILE", "")
	directory := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
	install := Install{Path: directory, Version: installerVersion}
	if ok, err := ProvisionManagedLicense(install); err != nil || ok {
		t.Fatalf("%v %v", ok, err)
	}
	old := filepath.Join(root, "7.6.0.1", platformDirectory(), "rti_connext_dds-7.6.0")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, LicenseFileName), []byte("legacy license"), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, err := ProvisionManagedLicense(install); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	canonical, _ := managedLicensePath()
	data, err := os.ReadFile(canonical)
	if err != nil || string(data) != "legacy license" {
		t.Fatalf("%q %v", data, err)
	}
	external := filepath.Join(t.TempDir(), LicenseFileName)
	if err := os.WriteFile(external, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTI_LICENSE_FILE", external)
	if err := os.Remove(LicenseFilePath(install)); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(external, LicenseFilePath(install)); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := ProvisionManagedLicense(install); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	data, _ = os.ReadFile(LicenseFilePath(install))
	if string(data) != "legacy license" {
		t.Fatal(string(data))
	}
	data, _ = os.ReadFile(external)
	if string(data) != "external" {
		t.Fatal("modified external license")
	}
	if !strings.Contains(strings.Join(install.EnvironmentOverrides(), "\n"), "RTI_LICENSE_FILE="+LicenseFilePath(install)) {
		t.Fatal("managed license not used by subprocess")
	}
	if len((Install{Path: t.TempDir()}).EnvironmentOverrides()) != 2 {
		t.Fatal("external license overridden")
	}
}

func TestInvalidSelectedLicenseBlocksFallback(t *testing.T) {
	root := managedTestEnvironment(t)
	dir := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
	external := t.TempDir()
	t.Setenv("NDDSHOME", external)
	if err := os.Mkdir(filepath.Join(external, LicenseFileName), 0700); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), LicenseFileName)
	if err := os.WriteFile(env, []byte("fallback"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTI_LICENSE_FILE", env)
	if _, err := ProvisionManagedLicense(Install{Path: dir}); err == nil || !strings.Contains(err.Error(), "fallback is disabled") {
		t.Fatal(err)
	}
}

func TestManagedValidationRequiresEveryTool(t *testing.T) {
	root := managedTestEnvironment(t)
	dir := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
	if err := os.Remove(Executable(dir, "rtiddsspy")); err != nil {
		t.Fatal(err)
	}
	if _, err := validateManaged(dir, installerVersion, normalizeOptions(DiscoveryOptions{})); err == nil {
		t.Fatal("accepted installation without Spy")
	}
}

func TestInstallerLockRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := lockInstallerFile(first); err != nil {
		t.Fatal(err)
	}
	if err := lockInstallerFile(second); err == nil {
		t.Fatal("concurrent lock acquired")
	}
	first.Close()
	if err := lockInstallerFile(second); err != nil {
		t.Fatalf("stale lock: %v", err)
	}
}

func TestInstallerTextInteractionAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	var log, progress bytes.Buffer
	interaction := &installerInteraction{out: &progress}
	script := `printf 'Do you accept this license? [y/n]: '; read answer; [ "$answer" = y ] || exit 10
printf 'Installation Directory [/tmp/example]: '; read answer; [ -z "$answer" ] || exit 11
printf 'Create an RTI Launcher shortcut on the Desktop [Y/n]: '; read answer; [ "$answer" = n ] || exit 12
printf '0%% 50%% 100%%\n########################################\n'
`
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := executeInstallerCommand(ctx, "sh", []string{"-c", script}, &log, &log, interaction.onOutput); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress.String(), "100%") {
		t.Fatal(progress.String())
	}
	chunked := &installerInteraction{out: io.Discard}
	if got := chunked.onOutput("Do you accept this lic"); got != "" {
		t.Fatal(got)
	}
	if got := chunked.onOutput("ense? [y/n]: "); got != "y\n" {
		t.Fatal(got)
	}
	cancelled, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	start := time.Now()
	if err := executeInstallerCommand(cancelled, "sh", []string{"-c", "sleep 30"}, io.Discard, io.Discard, nil); err == nil {
		t.Fatal("expected cancellation")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("installer process group did not stop promptly")
	}
}

func TestMountValidationAndChangedDiagnostics(t *testing.T) {
	dir := t.TempDir()
	for _, plist := range []string{"broken", `<plist><dict/></plist>`, fmt.Sprintf(`<plist><dict><key>mount-point</key><string>%s</string></dict></plist>`, t.TempDir())} {
		if err := validateMountPlist([]byte(plist), dir); err == nil {
			t.Fatal("accepted invalid mount")
		}
	}
	if err := validateMountPlist([]byte(fmt.Sprintf(`<plist><dict><key>mount-point</key><string>%s</string></dict></plist>`, dir)), dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "installbuilder_installer.log")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	before := snapshotInstallerLogs(dir)
	var output bytes.Buffer
	appendInstallerLogs(&output, dir, before)
	if output.Len() != 0 {
		t.Fatal("included stale diagnostics")
	}
	if err := os.WriteFile(path, []byte("new diagnostic"), 0600); err != nil {
		t.Fatal(err)
	}
	appendInstallerLogs(&output, dir, before)
	if !strings.Contains(output.String(), "new diagnostic") {
		t.Fatal(output.String())
	}
}

func TestMacOSInvalidMountStillDetachesAfterCancellation(t *testing.T) {
	oldPlatform, oldRun := Platform, runInstallerCommand
	t.Cleanup(func() { Platform, runInstallerCommand = oldPlatform, oldRun })
	Platform = func() (string, string) { return "darwin", "arm64" }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	detached := false
	runInstallerCommand = func(commandCtx context.Context, command string, args []string, stdout, stderr io.Writer, respond func(string) string) error {
		if command != "hdiutil" {
			t.Fatal("ran installer from invalid mount")
		}
		if args[0] == "attach" {
			fmt.Fprint(stdout, `<plist><dict/></plist>`)
			cancel()
		} else if args[0] == "detach" {
			detached = true
			if commandCtx.Err() != nil {
				t.Fatal("detach inherited cancelled context")
			}
		}
		return nil
	}
	if err := runManagedInstallerContext(ctx, "unused.dmg", t.TempDir(), io.Discard, io.Discard); err == nil {
		t.Fatal("accepted invalid mount")
	}
	if !detached {
		t.Fatal("did not detach")
	}
}

func TestLicenseRepairRefusesSymlinkedStoreParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires symlink privileges")
	}
	root := managedTestEnvironment(t)
	canonical, _ := managedLicensePath()
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Dir(canonical)); err != nil {
		t.Fatal(err)
	}
	if err := seedManagedLicense([]byte("private")); err == nil {
		t.Fatal("followed symlinked license directory")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside managed store")
	}
}

func TestInstallerCacheRejectsNonFileBeforeDownload(t *testing.T) {
	managedTestEnvironment(t)
	home, _ := UserHomeDir()
	cachePath := filepath.Join(home, "Downloads", "installer.run")
	if err := os.MkdirAll(cachePath, 0700); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("download started despite unusable cache path")
		return nil, nil
	})}
	if _, err := cachedInstaller(context.Background(), "https://example.invalid/installer.run", "unused", client, io.Discard); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected cache path error, got %v", err)
	}
}

func TestInstallerCacheFallsBackWhenDownloadsUnavailable(t *testing.T) {
	managedTestEnvironment(t)
	home, _ := UserHomeDir()
	if err := os.WriteFile(filepath.Join(home, "Downloads"), []byte("unrelated file"), 0600); err != nil {
		t.Fatal(err)
	}
	temporary := t.TempDir()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, temporary)
	}
	body := "installer fixture"
	client := &http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	path, err := cachedInstaller(context.Background(), "https://example.invalid/fallback.run", fmt.Sprintf("%x", sha256.Sum256([]byte(body))), client, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != temporary {
		t.Fatalf("expected temp fallback, got %s", path)
	}
	data, err := os.ReadFile(filepath.Join(home, "Downloads"))
	if err != nil || string(data) != "unrelated file" {
		t.Fatalf("modified Downloads entry: %q %v", data, err)
	}
}

func TestDownloadProgressMilestonesAndRendering(t *testing.T) {
	for _, inline := range []bool{false, true} {
		var out bytes.Buffer
		progress := &downloadProgress{out: &out, total: 100, inline: inline}
		for i := 0; i < 100; i++ {
			progress.Write([]byte{0})
		}
		progress.finish()
		text := out.String()
		if strings.Contains(text, "Download: 4%") || strings.Contains(text, "Download: 9%") || !strings.Contains(text, "Download: 5%") || !strings.Contains(text, "Download: 100%") {
			t.Fatal(text)
		}
		if inline {
			if strings.Count(text, "\n") != 1 || strings.Count(text, "\r") != 20 {
				t.Fatal(text)
			}
		} else if strings.Contains(text, "\r") || strings.Count(text, "\n") != 20 {
			t.Fatal(text)
		}
	}
}

func TestInterruptedDownloadProgressEndsLine(t *testing.T) {
	var out bytes.Buffer
	progress := &downloadProgress{out: &out, total: 100, inline: true}
	progress.Write(make([]byte, 17))
	progress.finish()
	progress.finish()
	if out.String() != "\rDownload: 15%\n" {
		t.Fatal(out.String())
	}
}

func TestInstallerProgressMilestonesAndRendering(t *testing.T) {
	for _, inline := range []bool{false, true} {
		var out bytes.Buffer
		interaction := &installerInteraction{out: &out, inline: inline, lastPercent: -1}
		interaction.onOutput("0% 50% 100%\n")
		for i := 0; i < 40; i++ {
			interaction.onOutput("#")
		}
		interaction.finish()
		text := out.String()
		if strings.Contains(text, "Installation: 2%") || strings.Contains(text, "Installation: 7%") || !strings.Contains(text, "Installation: 5%") || !strings.Contains(text, "Installation: 100%") {
			t.Fatal(text)
		}
		if inline {
			if strings.Count(text, "\n") != 1 || strings.Count(text, "\r") != 21 {
				t.Fatal(text)
			}
		} else if strings.Contains(text, "\r") || strings.Count(text, "\n") != 21 {
			t.Fatal(text)
		}
	}
}

func TestInstallerProgressEndsBeforeStatusAndOnFailure(t *testing.T) {
	for _, next := range []string{"", "Warning: test", "\nError: test", "Disable copying of examples to rti_workspace [Y/n]: "} {
		var out bytes.Buffer
		interaction := &installerInteraction{out: &out, inline: true, lastPercent: -1}
		interaction.onOutput("0% 50% 100%\n####")
		interaction.onOutput(next)
		interaction.finish()
		interaction.finish()
		if !strings.HasPrefix(out.String(), "\rInstallation: 10%\n") {
			t.Fatal(out.String())
		}
		if strings.Contains(out.String(), "\n\n") {
			t.Fatal("extra blank line: " + out.String())
		}
	}
}

func TestInstallerInteractionDeduplicatesRepeatedStatus(t *testing.T) {
	var out bytes.Buffer
	interaction := &installerInteraction{out: &out}
	interaction.onOutput("Disable copying of examples to rti_workspace [Y/n]: ")
	interaction.onOutput("Create an RTI Launcher shortcut on the Desktop [Y/n]: ")
	if strings.Count(out.String(), "Finalizing installation...") != 1 {
		t.Fatal(out.String())
	}
}
