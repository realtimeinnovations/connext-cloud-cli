// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

//go:build windows

package edgesyncagent

import (
	"context"
	"os"

	"golang.org/x/term"
)

// AgentSupported reports whether the long-running agent is supported on this
// platform.
func AgentSupported() bool { return false }

// startKeyReader restores canonical mode and disables raw keyboard handling.
// As with the other CLI prompts, Windows does not use raw escape sequences:
// ConPTY does not reliably deliver arrow-key input in that mode.
func startKeyReader(_ context.Context, inFile *os.File, oldState *term.State) (<-chan keyEvent, func()) {
	if oldState != nil {
		_ = term.Restore(int(inFile.Fd()), oldState)
	}
	return nil, nil
}
