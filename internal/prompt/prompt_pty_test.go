// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

//go:build !windows

package prompt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Use a child process so promptui owns a real terminal without changing the
// test runner's terminal mode or sharing stdin with another test.
func TestSelectorPTY(t *testing.T) {
	const childVariable = "RTICLOUD_PROMPT_PTY_CHILD"
	if os.Getenv(childVariable) == "1" {
		selector := Selector{In: os.Stdin, Out: os.Stdout}
		value, err := selector.Select("Choose fixture", []string{"first", "second"}, os.Getenv("RTICLOUD_PROMPT_PTY_DEFAULT"))
		if err != nil {
			fmt.Fprintf(os.Stdout, "PTY_ERROR:%s\n", err)
		} else {
			fmt.Fprintf(os.Stdout, "PTY_RESULT:%s\n", value)
		}
		return
	}

	for _, scenario := range []struct {
		name, defaultChoice, keys, want string
	}{
		{"enter-uses-default", "second", "\r", "PTY_RESULT:second"},
		{"down-then-enter", "first", "\x1b[B\r", "PTY_RESULT:second"},
		{"control-c-cancels", "first", "\x03", "PTY_ERROR:Selection cancelled."},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSelectorPTY$")
			cmd.Env = append(os.Environ(), childVariable+"=1", "RTICLOUD_PROMPT_PTY_DEFAULT="+scenario.defaultChoice, "TERM=xterm-256color")
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
			if err != nil {
				t.Fatal(err)
			}
			chunks := make(chan string, 8)
			readDone := make(chan struct{})
			go func() {
				defer close(readDone)
				defer close(chunks)
				buffer := make([]byte, 4096)
				for {
					n, err := terminal.Read(buffer)
					if n > 0 {
						select {
						case chunks <- string(buffer[:n]):
						case <-ctx.Done():
							return
						}
					}
					if err != nil {
						return // Unix PTYs report EOF or EIO when the child exits.
					}
				}
			}()
			waitDone := make(chan struct{})
			var waitErr error
			go func() {
				waitErr = cmd.Wait()
				close(waitDone)
			}()
			defer func() {
				cancel()
				terminal.Close()
				<-waitDone
				<-readDone
			}()

			var output strings.Builder
			sent := false
			for chunks != nil {
				select {
				case chunk, open := <-chunks:
					if !open {
						chunks = nil
						continue
					}
					output.WriteString(chunk)
					if !sent && strings.Contains(output.String(), "Choose fixture") && strings.Contains(output.String(), "second") {
						if _, err := terminal.Write([]byte(scenario.keys)); err != nil {
							t.Fatal(err)
						}
						sent = true
					}
				case <-ctx.Done():
					t.Fatalf("prompt timed out: %s", output.String())
				}
			}
			select {
			case <-waitDone:
			case <-ctx.Done():
				t.Fatalf("child did not exit: %s", output.String())
			}
			if waitErr != nil || !sent || !strings.Contains(output.String(), scenario.want) {
				t.Fatalf("child error=%v, want %q, output=%s", waitErr, scenario.want, output.String())
			}
		})
	}
}
