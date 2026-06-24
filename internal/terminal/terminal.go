// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package terminal

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func PlainOutputRequested(out io.Writer) bool {
	if envOptOut(os.Getenv("NO_COLOR")) || envOptOut(os.Getenv("CI")) || os.Getenv("TERM") == "dumb" {
		return true
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	return !term.IsTerminal(int(file.Fd()))
}

func PromptFiles(in io.Reader, out io.Writer) (*os.File, *os.File, bool) {
	if !SupportsPTY() {
		// On Windows, ConPTY doesn't reliably translate arrow-key escape
		// sequences (ESC [ A / ESC [ B) in raw mode — Enter works but
		// navigation is silently swallowed. Use the numbered fallback instead.
		return nil, nil, false
	}
	inputFile, ok := in.(*os.File)
	if !ok {
		return nil, nil, false
	}
	outputFile, ok := out.(*os.File)
	if !ok {
		return nil, nil, false
	}
	if !IsCharDevice(inputFile) || !IsCharDevice(outputFile) {
		return nil, nil, false
	}
	return inputFile, outputFile, true
}

func IsCharDevice(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func envOptOut(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no":
		return false
	default:
		return true
	}
}
