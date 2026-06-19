//go:build !windows

package terminal

import (
	"os"
	"os/exec"
	"syscall"
)

// PrepareProcess isolates the subprocess in its own process group so signals
// can be forwarded to the full process tree.
func PrepareProcess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// InterruptSignals returns the OS signals that indicate a user interrupt.
func InterruptSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// SendInterrupt sends SIGINT to the subprocess group for graceful shutdown.
func SendInterrupt(p *os.Process) {
	if p == nil || p.Pid <= 0 {
		return
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGINT); err == nil {
		return
	}
	_ = p.Signal(os.Interrupt)
}

// KillProcess forcefully stops the subprocess group.
func KillProcess(p *os.Process) {
	if p == nil || p.Pid <= 0 {
		return
	}
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err == nil {
		return
	}
	_ = p.Kill()
}

// ProcessRunning reports whether the process with the given PID is alive.
func ProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// PrepareCommand is a no-op on Unix
func PrepareCommand(cmd []string) []string { return cmd }
