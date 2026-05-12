//go:build !windows

package terminal

import (
	"os"
	"syscall"
)

// InterruptSignals returns the OS signals that indicate a user interrupt.
func InterruptSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// SendInterrupt sends SIGINT to the process for graceful shutdown.
func SendInterrupt(p *os.Process) {
	_ = p.Signal(os.Interrupt)
}

// ProcessRunning reports whether the process with the given PID is alive.
func ProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
