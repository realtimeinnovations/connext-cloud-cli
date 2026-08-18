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

// startKeyReader disables raw keyboard handling on Windows: the agent still
// runs, renders its live dashboard, renews artifacts and processes the
// enrollment inbox, but the in-dashboard key actions (arrow navigation,
// Enter-to-renew, Ctrl+A add) are not wired up. As with the other CLI prompts,
// Windows does not use raw escape sequences here: ConPTY does not reliably
// deliver arrow-key input in that mode. Returning (nil, nil) makes runDisplay
// fall back to its never-fires key channel, so the dashboard refreshes on the
// timer and stops on context cancellation (Ctrl+C is delivered as an OS signal;
// see Agent.Run). Adding a participant is still possible via `rticloud agent
// enroll`, which drops a request into the inbox.
func startKeyReader(_ context.Context, inFile *os.File, oldState *term.State) (<-chan keyEvent, func()) {
	if oldState != nil {
		_ = term.Restore(int(inFile.Fd()), oldState)
	}
	return nil, nil
}
