package tui

import (
	"fmt"
	"io"
	"strings"
)

const (
	beginSynchronizedUpdate = "\x1b[?2026h"
	endSynchronizedUpdate   = "\x1b[?2026l"
	hideCursorSequence      = "\x1b[?25l"
	showCursorSequence      = "\x1b[?25h"
)

// Screen repaints full-frame snapshots in place. Rows are diffed against the
// previous frame so only changed rows are rewritten, and each write is wrapped
// in a synchronized-update block so supporting terminals apply it atomically.
// The display is never cleared between frames, which keeps repaints
// flicker-free; stale cells are erased per row instead.
type Screen struct {
	out    io.Writer
	prev   []string
	width  int
	height int
	active bool
}

func NewScreen(out io.Writer) *Screen {
	return &Screen{out: out}
}

func (screen *Screen) Paint(lines []string, width int, height int) error {
	if screen == nil || screen.out == nil {
		return nil
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	fullRepaint := !screen.active || width != screen.width || height != screen.height
	previous := screen.prev
	if fullRepaint {
		previous = nil
	}
	var frame strings.Builder
	frame.WriteString(beginSynchronizedUpdate)
	if !screen.active {
		frame.WriteString(hideCursorSequence)
	}
	dirty := fullRepaint
	for index, line := range lines {
		if index < len(previous) && previous[index] == line {
			continue
		}
		dirty = true
		// Erase before writing: erasing after a full-width row would land on
		// the last column (wrap-pending) and blank the rightmost cell.
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[K%s", index+1, line)
	}
	if (fullRepaint && len(lines) < height) || len(previous) > len(lines) {
		dirty = true
		fmt.Fprintf(&frame, "\x1b[%d;1H\x1b[J", len(lines)+1)
	}
	frame.WriteString(endSynchronizedUpdate)
	screen.prev = append([]string(nil), lines...)
	screen.width = width
	screen.height = height
	screen.active = true
	if !dirty {
		return nil
	}
	_, err := io.WriteString(screen.out, frame.String())
	return err
}

// Finish re-enables the cursor and moves it to a fresh row below the frame so
// regular writes continue after the live view instead of on top of it. It is
// a no-op until the first Paint and after a previous Finish.
func (screen *Screen) Finish() error {
	if screen == nil || screen.out == nil || !screen.active {
		return nil
	}
	row := len(screen.prev)
	screen.prev = nil
	screen.active = false
	_, err := fmt.Fprintf(screen.out, "\x1b[%d;1H\n%s", row, showCursorSequence)
	return err
}
