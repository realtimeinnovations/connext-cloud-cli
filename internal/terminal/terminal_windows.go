//go:build windows

package terminal

import (
	"io"
	"os/exec"
)

// SupportsPTY reports whether the current platform supports a pseudo-terminal.
// On Windows, PTY is not supported.
func SupportsPTY() bool {
	return false
}

// StartProcess starts cmd with stdout/stderr pipes and returns them.
// Color output from subprocesses is not available on Windows.
func StartProcess(cmd *exec.Cmd) (io.ReadCloser, io.ReadCloser, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	PrepareProcess(cmd)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return stdout, stderr, nil
}
