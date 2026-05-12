//go:build !windows

package terminal

import (
	"io"
	"os/exec"

	"github.com/creack/pty"
)

// SupportsPTY reports whether the current platform supports a pseudo-terminal.
func SupportsPTY() bool {
	return true
}

// StartProcess starts cmd attached to a PTY and returns the master side as the
// combined stdout/stderr reader. The second return value is always nil on Unix.
func StartProcess(cmd *exec.Cmd) (io.ReadCloser, io.ReadCloser, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stdout = slave
	cmd.Stderr = slave
	if err := cmd.Start(); err != nil {
		_ = slave.Close()
		_ = master.Close()
		return nil, nil, err
	}
	_ = slave.Close()
	return master, nil, nil
}
