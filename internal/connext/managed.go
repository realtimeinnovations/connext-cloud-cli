// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/rtipaths"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

const installationPendingFile = ".rticloud-installing"

var (
	ManagedInstaller    = installManaged
	runInstallerCommand = executeInstallerCommand
	exactVersionRE      = regexp.MustCompile(`^\d+\.\d+\.\d+(?:\.\d+)?$`)
)

// Use Studio's Node platform names so both products use the same layout.
func platformDirectory() string {
	platform, arch := Platform()
	if platform == "windows" {
		platform = "win32"
	}
	if arch == "amd64" {
		arch = "x64"
	}
	return platform + "_" + arch
}

func managedRoot() (string, error) {
	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	if home == "" {
		return "", fmt.Errorf("cannot determine user home directory")
	}
	platform, _ := Platform()
	return filepath.Join(rtipaths.Root(home, platform, os.Getenv("LOCALAPPDATA")), "rticloud", "installations"), nil
}

type installHost struct {
	Version   string `xml:"base_version"`
	Directory string `xml:"install_dirname"`
}

func readInstallHost(directory string) (installHost, error) {
	data, err := os.ReadFile(filepath.Join(directory, "rti_versions.xml"))
	if err != nil {
		return installHost{}, err
	}
	var document struct {
		XMLName xml.Name    `xml:"rti"`
		Host    installHost `xml:"host"`
	}
	err = xml.Unmarshal(data, &document)
	return document.Host, err
}

// Refuse symlinks below the RTI root before writing or trusting managed content.
func safeManagedPath(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("installation path is outside RTI root: %s", target)
	}
	current := root
	for _, part := range append([]string{""}, strings.Split(relative, string(filepath.Separator))...) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed path must not contain symlinks: %s", current)
		}
	}
	return nil
}

func validateManaged(directory, version string, options DiscoveryOptions) (Install, error) {
	if _, err := os.Lstat(filepath.Join(filepath.Dir(directory), installationPendingFile)); !os.IsNotExist(err) {
		return Install{}, fmt.Errorf("Connext installation is incomplete at %s", directory)
	}
	metadata := filepath.Join(directory, "rti_versions.xml")
	if err := safeManagedPath(directory, metadata); err != nil {
		return Install{}, err
	}
	metadataInfo, err := os.Lstat(metadata)
	if err != nil {
		return Install{}, err
	}
	if !metadataInfo.Mode().IsRegular() {
		return Install{}, fmt.Errorf("invalid Connext metadata file: %s", metadata)
	}
	host, err := readInstallHost(directory)
	if err != nil {
		return Install{}, err
	}
	base := strings.Join(strings.Split(version, ".")[:3], ".")
	if host.Version != version || host.Directory != "rti_connext_dds-"+base || filepath.Base(directory) != host.Directory {
		return Install{}, fmt.Errorf("invalid Connext metadata at %s: expected version %s", directory, version)
	}
	install, err := ValidateInstall(directory, options)
	if err != nil {
		return Install{}, err
	}
	for _, tool := range []string{"rtiddsspy", "rtiroutingservice", "rticollectorservicelite"} {
		executable := Executable(directory, tool)
		if err := safeManagedPath(directory, executable); err != nil {
			return Install{}, err
		}
		info, err := os.Lstat(executable)
		if err != nil {
			return Install{}, err
		}
		platform, _ := Platform()
		if !info.Mode().IsRegular() || (platform != "windows" && info.Mode().Perm()&0o111 == 0) {
			return Install{}, fmt.Errorf("invalid Connext executable: %s", executable)
		}
	}
	install.Reason = "rticloud-managed installation"
	return install, nil
}

func verifyInstaller(file, digest string) error {
	return verifyInstallerContext(context.Background(), file, digest)
}

func verifyInstallerContext(ctx context.Context, file, digest string) error {
	input, err := os.Open(file)
	if err != nil {
		return err
	}
	defer input.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, contextReader{ctx: ctx, reader: input}); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("Connext installer SHA-256 verification failed")
	}
	return nil
}

func installManaged(options DiscoveryOptions) (Install, error) {
	artifact, err := bundledInstaller(Platform())
	if err != nil {
		return Install{}, err
	}
	return installManagedArtifact(options, artifact)
}

func installManagedArtifact(options DiscoveryOptions, artifact installerArtifact) (Install, error) {
	options = normalizeOptions(options)
	parent := options.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := signal.NotifyContext(parent, terminal.InterruptSignals()...)
	defer stop()
	out := options.Output
	if out == nil {
		out = os.Stdout
	}
	if CompareVersion(artifact.version, options.MinVersion) < 0 {
		return Install{}, fmt.Errorf("bundled Connext %s does not meet required version %s", artifact.version, options.MinVersion)
	}
	root, err := managedRoot()
	if err != nil {
		return Install{}, err
	}
	rti := filepath.Dir(filepath.Dir(root))
	directory := artifact.installationPath(root)
	prefix := filepath.Dir(directory)
	if err := safeManagedPath(rti, directory); err != nil {
		return Install{}, err
	}
	if install, err := validateManaged(directory, artifact.version, options); err == nil {
		return install, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Install{}, err
	}
	lockPath := filepath.Join(root, ".installer-"+artifact.version+"-"+artifact.platform+".lock")
	if err := safeManagedPath(rti, lockPath); err != nil {
		return Install{}, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Install{}, err
	}
	if err := lockInstallerFile(lock); err != nil {
		lock.Close()
		return Install{}, fmt.Errorf("another Connext setup is running: %w", err)
	}
	defer lock.Close() // OS releases this lock on cancellation or process exit too.
	if install, err := validateManaged(directory, artifact.version, options); err == nil {
		return install, nil
	}
	message := fmt.Sprintf("%s\nrticloud will install the Connext Professional %s components it needs to operate.\nThese do not modify your own Connext installation.\n\nInstallation folder:\n  %s\n\nChoosing ‘Accept license and install’ accepts the RTI License Agreement\non behalf of all users of the installed software.\n  https://www.rti.com/downloads/license-agreement.html\n\n%s\n\nContinue with installation?", tui.StyleStrong("Install required Connext components"), artifact.version, directory, tui.Dim(alternativeInstallationHint()))
	if options.Confirmations == nil {
		return Install{}, fmt.Errorf("%s\n\nRun interactively to confirm the download and license acceptance.", tui.StripANSIEscapes(message))
	}
	answer, err := options.Confirmations.Confirm(Confirmation{Message: message, DeclineLabel: CancelManagedDownloadLabel, AcceptLabel: AcceptManagedDownloadLabel})
	if err != nil {
		return Install{}, err
	}
	if !answer {
		return Install{}, fmt.Errorf("Connext installation cancelled.\n\n%s", alternativeInstallationHint())
	}
	installer, err := cachedInstaller(ctx, artifact.url, artifact.sha256, options.HTTPClient, out)
	if err != nil {
		return Install{}, err
	}
	logs := filepath.Join(filepath.Dir(root), "logs")
	if err := safeManagedPath(rti, logs); err != nil {
		return Install{}, err
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return Install{}, err
	}
	log, err := os.CreateTemp(logs, "connext-install-"+time.Now().UTC().Format("20060102T150405Z")+"-*.log")
	if err != nil {
		return Install{}, err
	}
	defer log.Close()
	fmt.Fprintf(log, "Connext setup started: %s\nPlatform: %s\nInstaller: %s\nDestination: %s\n", time.Now().UTC().Format(time.RFC3339), artifact.platform, installer, directory)
	if err := ctx.Err(); err != nil {
		return Install{}, err
	}
	// Repair only the exact current managed destination, never the whole prefix.
	if err := safeManagedPath(rti, directory); err != nil {
		return Install{}, err
	}
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() {
			return Install{}, fmt.Errorf("managed installation is not a directory: %s", directory)
		}
		// Preserve a readable license before replacing invalid binaries.
		if data, err := readCopyableLicense(filepath.Join(directory, LicenseFileName)); err == nil {
			if err := seedManagedLicense(data); err != nil {
				return Install{}, err
			}
		}
		if err := os.RemoveAll(directory); err != nil {
			return Install{}, err
		}
	} else if !os.IsNotExist(err) {
		return Install{}, err
	}

	if err := safeManagedPath(rti, prefix); err != nil {
		return Install{}, err
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return Install{}, err
	}
	pending := filepath.Join(prefix, installationPendingFile)
	if err := safeManagedPath(rti, pending); err != nil {
		return Install{}, err
	}
	if err := os.WriteFile(pending, nil, 0o600); err != nil {
		return Install{}, err
	}
	fmt.Fprintf(out, "Installing Connext Professional into %s\nInstallation log: %s\n", prefix, log.Name())
	if err := runManagedInstallerContext(ctx, installer, prefix, log, out); err != nil {
		return Install{}, fmt.Errorf("Connext installation failed (see %s): %w", log.Name(), err)
	}
	if err := safeManagedPath(rti, filepath.Join(directory, "bin", options.ExecutableName)); err != nil {
		return Install{}, err
	}
	if err := os.Remove(pending); err != nil {
		return Install{}, err
	}
	install, err := validateManaged(directory, artifact.version, options)
	if err != nil {
		_ = os.WriteFile(pending, nil, 0o600)
		return Install{}, fmt.Errorf("Connext installation validation failed (see %s): %w", log.Name(), err)
	}
	for _, warning := range pruneManagedInstallation(directory, options) {
		fmt.Fprintf(log, "WARNING: %v\n", warning)
	}
	// No old-version cleanup without authoritative requirements for all products.
	return install, nil
}

const (
	CancelManagedDownloadLabel = "Cancel"
	AcceptManagedDownloadLabel = "Accept license and install"
)

func alternativeInstallationHint() string {
	platform, arch := Platform()
	bundle := "<architecture>"
	switch platform {
	case "darwin":
		bundle = "arm64Darwin23clang16.0"
	case "linux":
		if arch == "arm64" {
			bundle = "armv8Linux4gcc8.5.0"
		} else {
			bundle = "x64Linux4gcc8.5.0"
		}
	case "windows":
		bundle = "x64Win64VS2017"
	}
	if platform == "windows" {
		return "To use another installation, set NDDSHOME or run its environment script in cmd.exe, then run rticloud in the same window:\n  call \"<installation-dir>\\resource\\scripts\\rtisetenv_" + bundle + ".bat\"\nIn PowerShell, set $env:NDDSHOME to the installation directory."
	}
	shell := filepath.Base(os.Getenv("SHELL"))
	extension := "bash"
	if shell == "zsh" || shell == "tcsh" {
		extension = shell
	}
	return "To use another installation, set NDDSHOME or source its environment script:\n  source \"<installation-dir>/resource/scripts/rtisetenv_" + bundle + "." + extension + "\""
}
