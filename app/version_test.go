package app

import (
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	output := VersionString()
	for _, want := range []string{"rticloud dev", "commit: none", "built: unknown"} {
		if !strings.Contains(output, want) {
			t.Fatalf("version output = %q, want %q", output, want)
		}
	}
}
