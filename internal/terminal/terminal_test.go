// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package terminal

import (
	"bytes"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadLinesHandlesSplitDelimitersAndFinalPartialLine(t *testing.T) {
	reader := iotest.OneByteReader(strings.NewReader("first\r\nsecond\rthird\nfourth"))
	var lines []string
	if err := ReadLines(reader, func(line string) { lines = append(lines, line) }); err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", "third", "fourth"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestReadLinesPreservesEmptyLines(t *testing.T) {
	var lines []string
	if err := ReadLines(strings.NewReader("first\n\nthird\n"), func(line string) { lines = append(lines, line) }); err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "", "third"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected lines: %#v", lines)
	}
}

func TestReadLinesHandlesLongFragmentedLine(t *testing.T) {
	want := strings.Repeat("x", 64*1024)
	var lines []string
	reader := iotest.OneByteReader(strings.NewReader(want + "\nnext"))
	if err := ReadLines(reader, func(line string) { lines = append(lines, line) }); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("unexpected long fragmented line count: %d", len(lines))
	}
	if lines[0] != want || lines[1] != "next" {
		t.Fatalf("unexpected long fragmented lines: lengths=%d,%d", len(lines[0]), len(lines[1]))
	}
}

func TestPlainOutputRequestedHonorsEnvironmentOptOut(t *testing.T) {
	var out bytes.Buffer
	t.Setenv("NO_COLOR", "1")
	if !PlainOutputRequested(&out) {
		t.Fatal("expected NO_COLOR to request plain output")
	}
}

func TestPlainOutputRequestedIgnoresFalseCIValue(t *testing.T) {
	var out bytes.Buffer
	t.Setenv("NO_COLOR", "")
	t.Setenv("CI", "false")
	t.Setenv("TERM", "xterm-256color")
	if PlainOutputRequested(&out) {
		t.Fatal("did not expect CI=false to request plain output")
	}
}

func TestCanAnimateFalseForBufferOutput(t *testing.T) {
	var out bytes.Buffer
	if CanAnimate(&out) {
		t.Fatal("did not expect animation for buffer output")
	}
	stop := StartSpinner(&out, "Loading...")
	stop()
	if out.Len() != 0 {
		t.Fatalf("expected no spinner output, got %q", out.String())
	}
}

func TestFormatSpinnerFrameUsesRTIOrange(t *testing.T) {
	got := formatSpinnerFrame("◐", "Loading...")
	want := "\r\033[38;5;208m◐\033[0m Loading..."
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
