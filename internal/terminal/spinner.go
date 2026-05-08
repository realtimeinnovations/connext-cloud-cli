package terminal

import (
	"fmt"
	"io"
	"os"
	"time"
)

var spinnerFrames = []string{"◐", "◓", "◑", "◒"}

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
			_, _ = fmt.Fprintf(out, "\r%s %s", spinnerFrames[frame%len(spinnerFrames)], message)
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