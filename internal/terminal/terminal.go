// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package terminal

import (
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ReadLines reads a stream incrementally and calls handle for each line. It
// accepts LF, CRLF, and CR delimiters and delivers a final unterminated line.
func ReadLines(reader io.Reader, handle func(string)) error {
	if reader == nil || handle == nil {
		return nil
	}
	buffer := make([]byte, 4096)
	pending := make([]byte, 0, len(buffer))
	scan := 0
	flush := func(force bool) {
		start := 0
		index := scan
		for index < len(pending) {
			switch pending[index] {
			case '\n':
				handle(string(pending[start:index]))
				index++
				start = index
			case '\r':
				if index+1 == len(pending) && !force {
					if start > 0 {
						copy(pending, pending[start:])
						pending = pending[:len(pending)-start]
						index -= start
					}
					scan = index
					return
				}
				handle(string(pending[start:index]))
				index++
				if index < len(pending) && pending[index] == '\n' {
					index++
				}
				start = index
			default:
				index++
			}
		}
		if start > 0 {
			copy(pending, pending[start:])
			pending = pending[:len(pending)-start]
			index -= start
		}
		scan = index
		if force && len(pending) > 0 {
			handle(string(pending))
			pending = pending[:0]
			scan = 0
		}
	}
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			pending = append(pending, buffer[:count]...)
			flush(false)
		}
		if err != nil {
			flush(true)
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

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
