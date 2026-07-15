// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

//go:build windows

package terminal

import (
	"os"
	"os/exec"
	"strings"
)

// PrepareProcess is a no-op on Windows.
func PrepareProcess(_ *exec.Cmd) {}

// InterruptSignals returns the OS signals that indicate a user interrupt.
// On Windows, only os.Interrupt (Ctrl+C) is available.
func InterruptSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// SendInterrupt is a no-op on Windows; the 2-second kill timer handles teardown.
func SendInterrupt(_ *os.Process) {}

// KillProcess forcefully stops the subprocess.
func KillProcess(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
}

// ProcessRunning reports whether the process with the given PID is alive.
// On Windows, os.FindProcess returns an error for non-existent PIDs.
func ProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}

// PrepareCommand wraps cmd in "cmd.exe /c" when the first argument is a .bat
// file. Windows cannot exec batch scripts directly; cmd.exe must interpret them.
func PrepareCommand(cmd []string) []string {
	if len(cmd) == 0 || !strings.HasSuffix(strings.ToLower(cmd[0]), ".bat") {
		return cmd
	}
	return append([]string{"cmd.exe", "/c"}, cmd...)
}
