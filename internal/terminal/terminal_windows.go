// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

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
