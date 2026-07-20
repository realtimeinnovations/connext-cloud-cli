// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

//go:build !windows

package edgesyncagent

import (
	"context"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// AgentSupported reports whether the long-running agent is supported on this
// platform.
func AgentSupported() bool { return true }

// startKeyReader puts inFile in raw mode and spawns a goroutine that sends
// recognised key events to the returned channel. It returns a stop func that
// restores the terminal and waits for the goroutine to exit; the caller must
// invoke stop before reading from inFile (e.g. for the enrollment wizard).
func startKeyReader(ctx context.Context, inFile *os.File, oldState *term.State) (<-chan keyEvent, func()) {
	ch := make(chan keyEvent, 4)

	// Wakeup pipe: writing to pw causes the goroutine's poll to return so it
	// exits cleanly without needing an additional keypress from the user.
	pr, pw, err := os.Pipe()
	if err != nil {
		close(ch)
		return ch, func() {}
	}

	stdinFd := int(inFile.Fd())

	send := func(k keyEvent) bool {
		select {
		case ch <- k:
			return true
		case <-ctx.Done():
			return false
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16)
		pollFds := []unix.PollFd{
			{Fd: int32(stdinFd), Events: unix.POLLIN},
			{Fd: int32(pr.Fd()), Events: unix.POLLIN},
		}
		for {
			_, err := unix.Poll(pollFds, -1)
			if err != nil {
				if err == syscall.EINTR {
					continue
				}
				return
			}
			// Wakeup pipe has data (or was closed) — time to stop.
			if pollFds[1].Revents != 0 {
				return
			}
			if pollFds[0].Revents&unix.POLLIN == 0 {
				continue
			}
			n, rerr := unix.Read(stdinFd, buf)
			if rerr != nil || n == 0 {
				return
			}
			i := 0
			for i < n {
				switch buf[i] {
				case 0x03: // Ctrl+C
					i++
					if !send(keyEvent{kind: keyStop}) {
						return
					}
				case 0x01: // Ctrl+A
					i++
					if !send(keyEvent{kind: keyAddProfile}) {
						return
					}
				case 0x09: // Tab → cycle profile
					i++
					if !send(keyEvent{kind: keyTab}) {
						return
					}
				case 0x0d, 0x0a: // Enter (CR or LF) → confirm
					i++
					if !send(keyEvent{kind: keyEnter}) {
						return
					}
				case 0x1b: // Escape — could be start of arrow-key sequence ESC [ X
					if i+2 < n && buf[i+1] == '[' {
						switch buf[i+2] {
						case 'A': // Up arrow
							i += 3
							if !send(keyEvent{kind: keyRowUp}) {
								return
							}
						case 'B': // Down arrow
							i += 3
							if !send(keyEvent{kind: keyRowDown}) {
								return
							}
						case 'D': // Left arrow
							i += 3
							if !send(keyEvent{kind: keyLeft}) {
								return
							}
						case 'C': // Right arrow
							i += 3
							if !send(keyEvent{kind: keyRight}) {
								return
							}
						default:
							i += 3 // skip unknown sequence
						}
					} else {
						i++ // lone ESC or incomplete — skip
					}
				default:
					if buf[i] >= '1' && buf[i] <= '9' {
						tab := int(buf[i] - '1')
						i++
						if !send(keyEvent{kind: keyJumpTab, num: tab}) {
							return
						}
					} else {
						i++ // unrecognised byte — skip
					}
				}
			}
		}
	}()

	stop := func() {
		// Signal the goroutine to exit by writing to the wakeup pipe, then wait.
		_, _ = pw.Write([]byte{0})
		_ = pw.Close()
		<-done
		_ = pr.Close()
		// Restore cooked mode after the goroutine has exited so the wizard
		// (or any subsequent reader) gets line-buffered canonical input.
		if oldState != nil {
			_ = term.Restore(stdinFd, oldState)
		}
	}
	return ch, stop
}
