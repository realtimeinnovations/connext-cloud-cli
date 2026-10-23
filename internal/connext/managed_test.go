// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/rtipaths"
)

const installerVersion = "7.7.0.1"

func managedTestEnvironment(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	oldHome, oldPlatform := UserHomeDir, Platform
	UserHomeDir = func() (string, error) { return home, nil }
	Platform = func() (string, string) {
		if runtime.GOOS == "windows" {
			return "windows", "amd64"
		}
		return "linux", "amd64"
	}
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "LocalAppData"))
	t.Cleanup(func() { UserHomeDir, Platform = oldHome, oldPlatform })
	root, err := managedRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeManagedFixture(t *testing.T, prefix, version string) string {
	t.Helper()
	directory := createConnextInstall(t, prefix, "7.7.0", "rtiroutingservice", "rtiddsspy", "rticollectorservicelite")
	writeVersions(t, directory, fmt.Sprintf(`<rti><host><base_version>%s</base_version><install_dirname>rti_connext_dds-7.7.0</install_dirname></host></rti>`, version))
	return directory
}

func TestRtiRoot(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		for _, appData := range []string{"", filepath.Join("custom", "local")} {
			want := filepath.Join("home", ".rti")
			if platform == "windows" {
				base := appData
				if base == "" {
					base = filepath.Join("home", "AppData", "Local")
				}
				want = filepath.Join(base, ".rti")
			}
			if got := rtipaths.Root("home", platform, appData); got != want {
				t.Fatalf("%s: %s != %s", platform, got, want)
			}
		}
	}
}

func TestManagedDiscoveryValidatesMetadataAndPlatform(t *testing.T) {
	root := managedTestEnvironment(t)
	expected := writeManagedFixture(t, filepath.Join(root, "7.7.0.1", platformDirectory()), "7.7.0.1")
	writeManagedFixture(t, filepath.Join(root, "9.0.0.1", platformDirectory()), "7.7.0.1")
	writeManagedFixture(t, filepath.Join(root, "7.7.0.2", "darwin_arm64"), "7.7.0.2")
	install, err := DiscoverInstall(map[string]string{}, DiscoveryOptions{})
	if err != nil || install.Path != expected || install.Version != "7.7.0.1" {
		t.Fatalf("%#v %v", install, err)
	}
	// An explicit environment override still takes precedence.
	custom := createConnextInstall(t, t.TempDir(), "7.8.0", "rtiroutingservice")
	install, err = DiscoverInstall(map[string]string{"NDDSHOME": custom}, DiscoveryOptions{AcceptExternalNDDSHome: true})
	if err != nil || install.Path != custom {
		t.Fatalf("%#v %v", install, err)
	}
}

func TestManagedRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires symlink privileges")
	}
	root := managedTestEnvironment(t)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := installManaged(DiscoveryOptions{Confirmations: ConfirmationFromSelector(func(string, []string, string) (string, error) {
		return AcceptManagedDownloadLabel, nil
	})}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManagedReusesValidInstallWithoutNetwork(t *testing.T) {
	root := managedTestEnvironment(t)
	expected := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
	oldGet := HTTPGet
	HTTPGet = func(string) (*http.Response, error) { t.Fatal("unexpected download"); return nil, nil }
	t.Cleanup(func() { HTTPGet = oldGet })
	install, err := installManaged(DiscoveryOptions{Confirmations: ConfirmationFromSelector(func(string, []string, string) (string, error) {
		t.Fatal("unexpected confirmation for a valid installation")
		return "", nil
	})})
	if err != nil || install.Path != expected {
		t.Fatalf("%#v %v", install, err)
	}
}

func TestManagedInstallVerifiedDownloadAndFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX test installer")
	}
	for _, scenario := range []string{"success", "repair", "repair-exit", "checksum", "exit", "metadata"} {
		t.Run(scenario, func(t *testing.T) {
			root := managedTestEnvironment(t)
			script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 if [ "$1" = "--prefix" ]; then shift; prefix="$1"; fi
 shift
done
mkdir -p "$prefix/rti_connext_dds-7.7.0/bin" "$prefix/rti_connext_dds-7.7.0/doc" "$prefix/rti_connext_dds-7.7.0/lib"
printf '#!/bin/sh\n' > "$prefix/rti_connext_dds-7.7.0/bin/rtiroutingservice"
chmod +x "$prefix/rti_connext_dds-7.7.0/bin/rtiroutingservice"
cp "$prefix/rti_connext_dds-7.7.0/bin/rtiroutingservice" "$prefix/rti_connext_dds-7.7.0/bin/rtiddsspy"
cp "$prefix/rti_connext_dds-7.7.0/bin/rtiroutingservice" "$prefix/rti_connext_dds-7.7.0/bin/rticollectorservicelite"
printf '<rti><host><base_version>7.7.0.1</base_version><install_dirname>rti_connext_dds-7.7.0</install_dirname></host></rti>' > "$prefix/rti_connext_dds-7.7.0/rti_versions.xml"
echo installed
`
			if scenario == "exit" || scenario == "repair-exit" {
				script = "#!/bin/sh\nexit 1\n"
			}
			if scenario == "metadata" {
				script = strings.ReplaceAll(script, "7.7.0.1", "7.6.0.1")
			}
			artifact, artifactErr := bundledInstaller("linux", "amd64")
			if artifactErr != nil {
				t.Fatal(artifactErr)
			}
			artifact.sha256 = fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
			if scenario == "checksum" {
				artifact.sha256 = "invalid"
			}
			getFixture := func(string) (*http.Response, error) {
				return &http.Response{StatusCode: 200, ContentLength: -1, Body: io.NopCloser(strings.NewReader(script))}, nil
			}
			var repairedDirectory, sibling string
			if scenario == "repair" || scenario == "repair-exit" {
				prefix := filepath.Join(root, installerVersion, platformDirectory())
				repairedDirectory = writeManagedFixture(t, prefix, installerVersion)
				if err := os.Remove(Executable(repairedDirectory, "rtiddsspy")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(Executable(repairedDirectory, "rtiroutingservice")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing-external-tool"), Executable(repairedDirectory, "rtiroutingservice")); err != nil {
					t.Fatal(err)
				}

				if err := os.WriteFile(filepath.Join(repairedDirectory, "incomplete"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(repairedDirectory, LicenseFileName), []byte("preserved license"), 0600); err != nil {
					t.Fatal(err)
				}
				sibling = filepath.Join(prefix, "unrelated")
				if err := os.WriteFile(sibling, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := installManagedArtifact(DiscoveryOptions{HTTPClient: &http.Client{Transport: testRoundTripper(func(r *http.Request) (*http.Response, error) { return getFixture(r.URL.String()) })}, Confirmations: ConfirmationFromSelector(func(string, []string, string) (string, error) {
				return AcceptManagedDownloadLabel, nil
			})}, artifact)
			if scenario == "success" || scenario == "repair" {
				if err != nil || result.Version != installerVersion {
					t.Fatalf("%#v %v", result, err)
				}
				if _, err := os.Stat(filepath.Join(result.Path, "doc")); !os.IsNotExist(err) {
					t.Fatal("successful installation was not trimmed")
				}
				if _, err := os.Stat(filepath.Join(result.Path, "lib")); !os.IsNotExist(err) {
					t.Fatal("library directory was not trimmed")
				}
				logs, _ := filepath.Glob(filepath.Join(filepath.Dir(root), "logs", "*.log"))
				if len(logs) != 1 {
					t.Fatal(logs)
				}
				data, _ := os.ReadFile(logs[0])
				if !strings.Contains(string(data), "installed") {
					t.Fatal(string(data))
				}
			} else if err == nil {
				t.Fatal("expected failure")
			}
			if scenario == "repair" || scenario == "repair-exit" {
				if _, err := os.Stat(filepath.Join(repairedDirectory, "incomplete")); !os.IsNotExist(err) {
					t.Fatal("incomplete tree was not replaced")
				}
				if _, err := os.Stat(sibling); err != nil {
					t.Fatal("repair removed sibling")
				}
				canonical, _ := managedLicensePath()
				if _, err := os.Lstat(canonical); !os.IsNotExist(err) {
					t.Fatalf("repair silently imported a license into the shared store: %v", err)
				}
				data, err := os.ReadFile(filepath.Join(repairedDirectory, LicenseFileName))
				if err != nil || string(data) != "preserved license" {
					t.Fatalf("license lost during repair: %q %v", data, err)
				}
			}
			if scenario == "metadata" {
				if _, err := os.Stat(filepath.Join(root, installerVersion, platformDirectory(), "rti_connext_dds-7.7.0", "doc")); err != nil {
					t.Fatal("invalid installation was trimmed")
				}
			}
			if scenario == "checksum" {
				if _, err := os.Stat(filepath.Join(root, installerVersion)); !os.IsNotExist(err) {
					t.Fatalf("unverified installer executed: %v", err)
				}
			}

			lock, err := os.OpenFile(filepath.Join(root, ".installer-"+installerVersion+"-"+platformDirectory()+".lock"), os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := lockInstallerFile(lock); err != nil {
				t.Fatalf("lock was not released: %v", err)
			}

		})
	}
}

func TestVersionIgnoresUnrelatedParentVersion(t *testing.T) {
	directory := createConnextInstall(t, filepath.Join(t.TempDir(), "1.2.3"), "7.7.0", "rtiroutingservice")
	install, err := ValidateInstall(directory, DiscoveryOptions{})
	if err != nil || install.Version != "7.7.0" {
		t.Fatalf("%#v %v", install, err)
	}
}

func TestPlatformInstallerCommands(t *testing.T) {
	for _, platform := range []string{"linux", "windows", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			oldPlatform, oldRun := Platform, runInstallerCommand
			Platform = func() (string, string) { return platform, "arm64" }
			t.Cleanup(func() { Platform, runInstallerCommand = oldPlatform, oldRun })
			installer := filepath.Join(t.TempDir(), "installer")
			if err := os.WriteFile(installer, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			prefix := filepath.Join(t.TempDir(), "space in prefix")
			var calls []string
			runInstallerCommand = func(ctx context.Context, command string, args []string, output, stderr io.Writer, respond func(string) string) error {
				calls = append(calls, command)
				if command == "hdiutil" {
					if args[0] == "attach" {
						mount := args[5]
						fmt.Fprintf(output, "<plist><dict><key>mount-point</key><string>%s</string></dict></plist>", mount)
						app := filepath.Join(mount, "Installer.app", "Contents", "MacOS")
						if err := os.MkdirAll(app, 0o755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(app, "installbuilder.sh"), nil, 0o700); err != nil {
							t.Fatal(err)
						}
					} else if args[0] == "detach" {
						if err := os.RemoveAll(filepath.Join(args[1], "Installer.app")); err != nil {
							t.Fatal(err)
						}
					}
					return nil
				}
				if platform == "windows" {
					if strings.Join(args, "|") != "--allow_unattended|true|--mode|unattended|--unattendedmodeui|minimalWithDialogs|--prefix|"+prefix+"|--disable_copy_examples|true" {
						t.Fatal(args)
					}
				} else if strings.Join(args, "|") != "--mode|text|--prefix|"+prefix {
					t.Fatal(args)
				}
				// Even on installer failure macOS must detach its image.
				return fmt.Errorf("installer failed")
			}
			if err := runManagedInstaller(installer, prefix, io.Discard); err == nil {
				t.Fatal("expected installer error")
			}
			if platform == "darwin" {
				if len(calls) != 3 || calls[0] != "hdiutil" || calls[2] != "hdiutil" {
					t.Fatal(calls)
				}
			} else if len(calls) != 1 || calls[0] != installer {
				t.Fatal(calls)
			}
		})
	}
}

func TestPendingInstallationNeedsConfirmationBeforeRepair(t *testing.T) {
	root := managedTestEnvironment(t)
	prefix := filepath.Join(root, installerVersion, platformDirectory())
	writeManagedFixture(t, prefix, installerVersion)
	if err := os.WriteFile(filepath.Join(prefix, installationPendingFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverInstall(map[string]string{}, DiscoveryOptions{}); err == nil {
		t.Fatal("discovered unfinished installation")
	}
	if _, err := installManaged(DiscoveryOptions{}); err == nil || !strings.Contains(err.Error(), "confirm the download") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(prefix); err != nil {
		t.Fatal("existing installation removed")
	}
}

func TestMissingManagedInstallRequiresConsentBeforeDownload(t *testing.T) {
	for _, mode := range []string{"cancel", "no-prompt", "prompt-error"} {
		t.Run(mode, func(t *testing.T) {
			root := managedTestEnvironment(t)
			oldGet := HTTPGet
			t.Cleanup(func() { HTTPGet = oldGet })
			HTTPGet = func(string) (*http.Response, error) { t.Fatal("download occurred without consent"); return nil, nil }
			options := DiscoveryOptions{AcceptExternalNDDSHome: true}
			if mode != "no-prompt" {
				options.Confirmations = ConfirmationFromSelector(func(message string, choices []string, _ string) (string, error) {
					if len(choices) != 2 || choices[0] != CancelManagedDownloadLabel || choices[1] != AcceptManagedDownloadLabel {
						t.Fatal(choices)
					}
					if mode == "prompt-error" {
						return "", fmt.Errorf("input closed")
					}
					return CancelManagedDownloadLabel, nil
				})
			}
			if _, err := installManaged(options); err == nil {
				t.Fatal("expected cancellation or missing consent")
			}
			if _, err := os.Stat(filepath.Join(root, installerVersion, platformDirectory())); !os.IsNotExist(err) {
				t.Fatal("installation prefix created without consent")
			}
		})
	}
}

func TestAlternativeInstallationHintUsesHostScript(t *testing.T) {
	old := Platform
	t.Cleanup(func() { Platform = old })
	for _, test := range []struct{ platform, arch, shell, script string }{
		{"darwin", "arm64", "/bin/zsh", "rtisetenv_arm64Darwin23clang16.0.zsh"},
		{"darwin", "arm64", "/bin/bash", "rtisetenv_arm64Darwin23clang16.0.bash"},
		{"linux", "amd64", "/bin/bash", "rtisetenv_x64Linux4gcc8.5.0.bash"},
		{"linux", "arm64", "/bin/tcsh", "rtisetenv_armv8Linux4gcc8.5.0.tcsh"},
		{"windows", "amd64", "", "rtisetenv_x64Win64VS2017.bat"},
	} {
		Platform = func() (string, string) { return test.platform, test.arch }
		t.Setenv("SHELL", test.shell)
		hint := alternativeInstallationHint()
		if !strings.Contains(hint, test.script) {
			t.Fatalf("incorrect script: %s", hint)
		}
		if test.platform == "windows" && !strings.Contains(hint, "cmd.exe") {
			t.Fatal(hint)
		}
		if test.platform != "windows" && !strings.Contains(hint, "source ") {
			t.Fatal(hint)
		}
	}
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestManagedRepairRecoversLicenseAfterAbruptExit(t *testing.T) {
	const childHome = "RTICLOUD_TEST_REPAIR_HOME"
	if home := os.Getenv(childHome); home != "" {
		UserHomeDir = func() (string, error) { return home, nil }
		Platform = func() (string, string) { return "linux", "amd64" }
	} else {
		managedTestEnvironment(t)
	}
	root, err := managedRoot()
	if err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(root, installerVersion, platformDirectory())
	script := "#!/bin/sh\nexit 1\n"
	artifact, err := bundledInstaller("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	artifact.sha256 = fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
	options := DiscoveryOptions{
		HTTPClient: &http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, ContentLength: -1, Body: io.NopCloser(strings.NewReader(script))}, nil
		})},
		Confirmations: ConfirmFunc(func(Confirmation) (bool, error) { return true, nil }),
		Output:        io.Discard,
	}
	oldRun := runInstallerCommand
	t.Cleanup(func() { runInstallerCommand = oldRun })
	if os.Getenv(childHome) != "" {
		runInstallerCommand = func(context.Context, string, []string, io.Writer, io.Writer, func(string) string) error {
			os.Exit(23) // Abrupt exit bypasses every restoration defer.
			return nil
		}
		_, err := installManagedArtifact(options, artifact)
		t.Fatalf("expected abrupt exit, got %v", err)
	}
	directory := writeManagedFixture(t, prefix, installerVersion)
	license := filepath.Join(directory, LicenseFileName)
	if err := os.WriteFile(license, []byte("installation-only license"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(Executable(directory, "rtiddsspy")); err != nil {
		t.Fatal(err)
	}
	home, _ := UserHomeDir()
	child := exec.Command(os.Args[0], "-test.run=^TestManagedRepairRecoversLicenseAfterAbruptExit$")
	child.Env = append(os.Environ(), childHome+"="+home)
	if output, err := child.CombinedOutput(); err == nil {
		t.Fatal("expected abrupt exit")
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
		t.Fatalf("child: %v\n%s", err, output)
	}
	if _, err := os.Stat(license); !os.IsNotExist(err) {
		t.Fatalf("installer had not removed the old license: %v", err)
	}
	recovery := installationLicenseRecoveryPath(directory)
	if data, err := os.ReadFile(recovery); err != nil || string(data) != "installation-only license" {
		t.Fatalf("recovery copy: %q %v", data, err)
	}
	runInstallerCommand = func(context.Context, string, []string, io.Writer, io.Writer, func(string) string) error {
		writeManagedFixture(t, prefix, installerVersion)
		return nil
	}
	if _, err := installManagedArtifact(options, artifact); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(license); err != nil || string(data) != "installation-only license" {
		t.Fatalf("restored license: %q %v", data, err)
	}
	if _, err := os.Stat(recovery); !os.IsNotExist(err) {
		t.Fatalf("recovery copy was not cleaned up: %v", err)
	}
	canonical, _ := managedLicensePath()
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Fatalf("repair silently imported license: %v", err)
	}
}

func TestManagedDiscoveryRecoversLicenseBeforeReusingInstall(t *testing.T) {
	root := managedTestEnvironment(t)
	directory := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
	recovery := installationLicenseRecoveryPath(directory)
	if err := writeVerifiedLicense(recovery, []byte("recover me")); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverInstall(map[string]string{}, DiscoveryOptions{HTTPClient: &http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Fatal("recovery must not download an installer")
		return nil, nil
	})}}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, LicenseFileName)); err != nil || string(data) != "recover me" {
		t.Fatalf("restored license: %q %v", data, err)
	}
	if _, err := os.Stat(recovery); !os.IsNotExist(err) {
		t.Fatalf("recovery copy was not cleaned up: %v", err)
	}
}

func TestManagedHealthyInstallBypassesInstallerLock(t *testing.T) {
	root := managedTestEnvironment(t)
	artifact, err := bundledInstaller("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	directory := writeManagedFixture(t, filepath.Dir(artifact.installationPath(root)), installerVersion)
	lock, err := os.OpenFile(filepath.Join(root, ".installer-"+artifact.version+"-"+artifact.platform+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := lockInstallerFile(lock); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"", filepath.Join(filepath.Dir(directory), installationPendingFile), installationLicenseRecoveryPath(directory)} {
		if marker != "" {
			if err := os.WriteFile(marker, []byte("pending recovery"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		install, err := installManagedArtifact(DiscoveryOptions{Output: io.Discard}, artifact)
		if marker == "" {
			if err != nil || install.Path != directory {
				t.Fatalf("healthy installation: %+v %v", install, err)
			}
		} else {
			if err == nil || !strings.Contains(err.Error(), "another Connext setup is running") {
				t.Fatalf("marker %s bypassed lock: %v", marker, err)
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestManagedRepairUsesSavedLicenseForLocalSymlink(t *testing.T) {
	for _, scenario := range []string{"readable-target", "dangling-target", "missing-saved", "symlinked-saved"} {
		t.Run(scenario, func(t *testing.T) {
			root := managedTestEnvironment(t)
			artifact, err := bundledInstaller("linux", "amd64")
			if err != nil {
				t.Fatal(err)
			}
			prefix := filepath.Dir(artifact.installationPath(root))
			directory := writeManagedFixture(t, prefix, installerVersion)
			if err := os.Remove(Executable(directory, "rtiddsspy")); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside.dat")
			if scenario != "dangling-target" {
				if err := os.WriteFile(outside, []byte("unrelated"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			license := filepath.Join(directory, LicenseFileName)
			if err := os.Symlink(outside, license); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			saved, _ := managedLicensePath()
			if scenario != "missing-saved" {
				if err := writeVerifiedLicense(saved, []byte(validTestConnextLicense)); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "symlinked-saved" {
				if err := os.Remove(saved); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, saved); err != nil {
					t.Fatal(err)
				}
			}
			script := "#!/bin/sh\nexit 1\n"
			artifact.sha256 = fmt.Sprintf("%x", sha256.Sum256([]byte(script)))
			oldRun := runInstallerCommand
			t.Cleanup(func() { runInstallerCommand = oldRun })
			ran := false
			runInstallerCommand = func(context.Context, string, []string, io.Writer, io.Writer, func(string) string) error {
				ran = true
				writeManagedFixture(t, prefix, installerVersion)
				return nil
			}
			_, err = installManagedArtifact(DiscoveryOptions{
				Output:        io.Discard,
				Confirmations: ConfirmFunc(func(Confirmation) (bool, error) { return true, nil }),
				HTTPClient: &http.Client{Transport: testRoundTripper(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, ContentLength: -1, Body: io.NopCloser(strings.NewReader(script))}, nil
				})},
			}, artifact)
			if scenario == "missing-saved" || scenario == "symlinked-saved" {
				if err == nil || ran {
					t.Fatalf("unsafe repair proceeded: ran=%v err=%v", ran, err)
				}
				if target, err := os.Readlink(license); err != nil || target != outside {
					t.Fatalf("local symlink changed: %q %v", target, err)
				}
			} else {
				if err != nil || !ran {
					t.Fatalf("repair failed: ran=%v err=%v", ran, err)
				}
				if data, err := readCopyableLicense(license); err != nil || string(data) != validTestConnextLicense {
					t.Fatalf("local license: %q %v", data, err)
				}
				if data, err := readCopyableLicense(saved); err != nil || string(data) != validTestConnextLicense {
					t.Fatalf("saved license changed: %q %v", data, err)
				}
			}
			if scenario == "dangling-target" {
				if _, err := os.Lstat(outside); !os.IsNotExist(err) {
					t.Fatalf("dangling target created: %v", err)
				}
			} else if data, err := os.ReadFile(outside); err != nil || string(data) != "unrelated" {
				t.Fatalf("external target changed: %q %v", data, err)
			}
		})
	}
}
