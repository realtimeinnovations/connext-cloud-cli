// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package terminal

import (
	"fmt"
	"io"
	"os"
	"time"
)

var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

const (
	spinnerRTIOrange = "\033[38;5;208m"
	spinnerReset     = "\033[0m"
)

func CanAnimate(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	if !IsCharDevice(file) {
		return false
	}
	return !PlainOutputRequested(out)
}

func StartSpinner(out io.Writer, message string) func() {
	if out == nil || !CanAnimate(out) {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		frame := 0
		for {
			_, _ = fmt.Fprint(out, formatSpinnerFrame(spinnerFrames[frame%len(spinnerFrames)], message))
			frame++
			select {
			case <-ticker.C:
			case <-stop:
				_, _ = fmt.Fprint(out, "\r\033[K")
				close(done)
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

func formatSpinnerFrame(frame, message string) string {
	return "\r" + spinnerRTIOrange + frame + spinnerReset + " " + message
}
