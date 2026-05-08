package terminal

import (
	"bytes"
	"testing"
)

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
