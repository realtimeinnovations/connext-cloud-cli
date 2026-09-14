// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/terminal"
)

func executeInstallerCommand(ctx context.Context, command string, args []string, stdout, stderr io.Writer, respond func(string) string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	terminal.PrepareProcess(cmd)
	cmd.Cancel = func() error { terminal.KillProcess(cmd.Process); return nil }
	cmd.WaitDelay = 5 * time.Second
	if respond != nil {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		defer stdin.Close()
		writer := &installerOutput{log: stdout, stdin: stdin, respond: respond}
		cmd.Stdout = writer
		cmd.Stderr = writer
	} else {
		cmd.Stdout = stdout
		cmd.Stderr = stderr
	}
	return cmd.Run()
}

type installerOutput struct {
	log     io.Writer
	stdin   io.Writer
	respond func(string) string
}

func (w *installerOutput) Write(data []byte) (int, error) {
	if _, err := w.log.Write(data); err != nil {
		return 0, err
	}
	if response := w.respond(string(data)); response != "" {
		if _, err := io.WriteString(w.stdin, response); err != nil {
			return 0, err
		}
	}
	return len(data), nil
}

type installerInteraction struct {
	pending               string
	progress              bool
	warned, reportedError bool
	hashes, lastPercent   int
	lastStatus            string
	out                   io.Writer
	inline, rendered      bool
}

var installerErrorPattern = regexp.MustCompile(`(?im)(?:^|\n)\s*Error:`)

var installerPrompts = []struct {
	pattern          *regexp.Regexp
	response, status string
}{
	{regexp.MustCompile(`(?i)Do you accept this license\?\s*\[y/n\]:\s*$`), "y\n", "Accepting the RTI License Agreement..."},
	{regexp.MustCompile(`(?i)Installation Directory\s*\[[^\]]+\]:\s*$`), "\n", "Using the managed installation folder."},
	{regexp.MustCompile(`(?i)Do you want to continue\?\s*\[Y/n\]:\s*$`), "\n", "Installing Connext Professional..."},
	{regexp.MustCompile(`(?i)Disable copying of examples to rti_workspace\s*\[Y/n\]:\s*$`), "\n", "Finalizing installation..."},
	{regexp.MustCompile(`(?i)Create an RTI Launcher shortcut on the Desktop\s*\[Y/n\]:\s*$`), "n\n", "Finalizing installation..."},
	{regexp.MustCompile(`(?i)Press \[Enter\] to continue:\s*$`), "\n", ""},
}

func (p *installerInteraction) onOutput(data string) string {
	p.pending += data
	if len(p.pending) > 8192 {
		p.pending = p.pending[len(p.pending)-8192:]
	}
	if !p.progress && strings.Contains(p.pending, "0%") && strings.Contains(p.pending, "50%") && strings.Contains(p.pending, "100%") {
		p.progress = true
		// Start counting only content after the scale, not license-text hashes.
		data = p.pending[strings.LastIndex(p.pending, "100%")+4:]
	}
	if p.progress {
		p.hashes += strings.Count(data, "#")
		percent := min(100, p.hashes*100/40) / 5 * 5
		if percent > p.lastPercent {
			if p.inline {
				fmt.Fprintf(p.out, "\rInstallation: %d%%", percent)
				p.rendered = true
			} else {
				fmt.Fprintf(p.out, "Installation: %d%%\n", percent)
			}
			p.lastPercent = percent
		}
	}
	if !p.warned && strings.Contains(strings.ToLower(p.pending), "warning:") {
		p.warned = true
		p.finish()
		fmt.Fprintln(p.out, "Installer reported a warning; see the installation log.")
	}
	if !p.reportedError && installerErrorPattern.MatchString(p.pending) {
		p.reportedError = true
		p.finish()
		fmt.Fprintln(p.out, "Installer reported an error; see the installation log.")
	}
	for _, prompt := range installerPrompts {
		if prompt.pattern.MatchString(p.pending) {
			p.pending = ""
			p.reportStatus(prompt.status)
			return prompt.response
		}
	}
	return ""
}

func (p *installerInteraction) reportStatus(status string) {
	if status == "" || status == p.lastStatus {
		return
	}
	p.finish()
	fmt.Fprintln(p.out, status)
	p.lastStatus = status
}

// finish separates subsequent status/error output from the live progress line.
func (p *installerInteraction) finish() {
	if p.rendered {
		fmt.Fprintln(p.out)
		p.rendered = false
	}
}

func runManagedInstaller(installer, prefix string, log io.Writer) error {
	return runManagedInstallerContext(context.Background(), installer, prefix, log, io.Discard)
}

func runManagedInstallerContext(ctx context.Context, installer, prefix string, log, out io.Writer) (result error) {
	before := snapshotInstallerLogs(os.TempDir())
	defer appendInstallerLogs(log, os.TempDir(), before)
	platform, _ := Platform()
	args := []string{"--mode", "text", "--prefix", prefix}
	interaction := &installerInteraction{out: out, lastPercent: -1, inline: terminal.CanAnimate(out)}
	defer interaction.finish()
	var respond func(string) string = interaction.onOutput
	switch platform {
	case "windows":
		args = []string{"--allow_unattended", "true", "--mode", "unattended", "--unattendedmodeui", "minimalWithDialogs", "--prefix", prefix, "--disable_copy_examples", "true"}
		respond = nil
		fmt.Fprintln(out, "Installing Connext Professional...")
	case "linux":
		if err := os.Chmod(installer, 0o700); err != nil {
			return err
		}
	case "darwin":
		mount, err := os.MkdirTemp("", "rticloud-connext-mount-")
		if err != nil {
			return err
		}
		var plist bytes.Buffer
		if err := runInstallerCommand(ctx, "hdiutil", []string{"attach", "-nobrowse", "-readonly", "-plist", "-mountpoint", mount, installer}, &plist, log, nil); err != nil {
			_ = os.Remove(mount)
			return err
		}
		defer func() {
			// Detach must still run after an interrupt cancels the install context.
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := runInstallerCommand(cleanup, "hdiutil", []string{"detach", mount}, log, log, nil); err != nil {
				result = errors.Join(result, fmt.Errorf("unable to detach installer image at %s: %w", mount, err))
			} else {
				_ = os.Remove(mount)
			}
		}()
		if err := validateMountPlist(plist.Bytes(), mount); err != nil {
			return err
		}
		fmt.Fprintf(log, "Installer image mounted at %s\n", mount)
		var candidates []string
		err = filepath.WalkDir(mount, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type().IsRegular() && entry.Name() == "installbuilder.sh" && strings.HasSuffix(path, filepath.Join("Contents", "MacOS", "installbuilder.sh")) && strings.Contains(path, ".app"+string(filepath.Separator)) {
				candidates = append(candidates, path)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if len(candidates) != 1 {
			return fmt.Errorf("expected one macOS installer, found %d", len(candidates))
		}
		installer = candidates[0]
	default:
		return fmt.Errorf("unsupported installer platform: %s", platform)
	}
	return runInstallerCommand(ctx, installer, args, log, log, respond)
}

func validateMountPlist(data []byte, expected string) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var mounts []string
	wantMount := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid hdiutil plist: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "key" {
			var key string
			if err := decoder.DecodeElement(&key, &start); err != nil {
				return err
			}
			wantMount = key == "mount-point"
		} else if wantMount {
			if start.Name.Local != "string" {
				return fmt.Errorf("invalid hdiutil mount-point")
			}
			var mount string
			if err := decoder.DecodeElement(&mount, &start); err != nil {
				return err
			}
			mounts = append(mounts, mount)
			wantMount = false
		}
	}
	if len(mounts) != 1 || !filepath.IsAbs(mounts[0]) {
		return fmt.Errorf("expected one absolute hdiutil mount-point")
	}
	actual, err := filepath.EvalSymlinks(mounts[0])
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(expected)
	if err != nil {
		return err
	}
	if actual != canonical {
		return fmt.Errorf("hdiutil mounted at an unexpected path: %s", actual)
	}
	return nil
}

type installerLogStamp struct {
	size     int64
	modified time.Time
}

var installerLogPattern = regexp.MustCompile(`^installbuilder_installer(?:_\d+)?\.log$`)

func snapshotInstallerLogs(directory string) map[string]installerLogStamp {
	result := map[string]installerLogStamp{}
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !installerLogPattern.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		result[entry.Name()] = installerLogStamp{info.Size(), info.ModTime()}
	}
	return result
}
func appendInstallerLogs(out io.Writer, directory string, before map[string]installerLogStamp) {
	for name, stamp := range snapshotInstallerLogs(directory) {
		if old, ok := before[name]; ok && old == stamp {
			continue
		}
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		input, err := os.Open(path)
		if err != nil {
			fmt.Fprintf(out, "WARNING: unable to read InstallBuilder diagnostics: %v\n", err)
			continue
		}
		fmt.Fprintf(out, "\n--- InstallBuilder diagnostics: %s ---\n", path)
		_, err = io.Copy(out, input)
		input.Close()
		if err != nil {
			fmt.Fprintf(out, "\nWARNING: unable to copy diagnostics: %v\n", err)
		}
		fmt.Fprintln(out, "\n--- End InstallBuilder diagnostics ---")
	}
}
