// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

//go:build !windows

package edgesyncagent

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func TestReadTokenInput_LongTerminalPaste(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	before, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}

	// Exceed macOS's canonical input limit, and Linux's larger limit too.
	token := buildJWT(map[string]any{
		"edge_system_id": "test-service",
		"participant_id": "test-participant",
		"padding":        strings.Repeat("x", 6000),
	})
	var output bytes.Buffer
	a := &Agent{In: slave, Out: &output}
	type result struct {
		token string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		got, err := a.readTokenInput("Campaign token")
		done <- result{got, err}
	}()

	// Wait until hidden input is active before simulating a paste.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		state, err := term.GetState(int(slave.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		if *state != *before {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("hidden input did not become active")
		}
	}
	if _, err := master.Write([]byte(token + "\r")); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.token != token {
			t.Fatalf("token truncated: got %d bytes, want %d", len(got.token), len(token))
		}
	case <-deadline.C:
		// Clear a full canonical input buffer so a broken reader can exit.
		_, _ = master.Write([]byte("\x15\n"))
		t.Fatal("long token paste hung waiting for Enter")
	}
	after, err := term.GetState(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if *after != *before {
		t.Fatal("terminal settings were not restored")
	}
	if strings.Contains(output.String(), token) {
		t.Fatal("token was echoed to prompt output")
	}
	// Raw input must not echo the token through the terminal driver either.
	fd := int(master.Fd())
	if err := unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(token)+1024)
	n, _ := unix.Read(fd, buf)
	if n > 0 {
		t.Fatal("token was echoed by the terminal")
	}
}
