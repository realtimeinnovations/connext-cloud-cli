//go:build windows

package terminal

import (
	"os"
	"strings"
)

// InterruptSignals returns the OS signals that indicate a user interrupt.
// On Windows, only os.Interrupt (Ctrl+C) is available.
func InterruptSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// SendInterrupt is a no-op on Windows; the 2-second kill timer handles teardown.
func SendInterrupt(_ *os.Process) {}

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
