package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/cli"
)

func TestRuntimeExecuteVersion(t *testing.T) {
	var out bytes.Buffer
	runtime := NewRuntime("", &out)

	if err := runtime.Execute(cli.Args{Resource: "version"}); err != nil {
		t.Fatal(err)
	}

	output := out.String()
	for _, want := range []string{"rticloud dev", "commit: none", "built: unknown"} {
		if !strings.Contains(output, want) {
			t.Fatalf("version output = %q, want %q", output, want)
		}
	}
}
