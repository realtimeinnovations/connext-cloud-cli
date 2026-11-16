// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
)

func TestLicenseUseJSONAndExplicitReplacement(t *testing.T) {
	rt := doctorRuntime(t)
	source := filepath.Join(t.TempDir(), "chosen.dat")
	content := []byte("FEATURE CONNEXTPRO RTI 7. permanent SIGN=TEST_ONLY\n")
	if err := os.WriteFile(source, content, 0600); err != nil {
		t.Fatal(err)
	}
	rt.License = nil // Local selection must work without the download client.
	var out bytes.Buffer
	if err := Execute([]string{"license", "use", source, "--format", "json"}, &out, io.Discard, rt); err != nil {
		t.Fatal(err)
	}
	var result struct {
		SchemaVersion string `json:"schema_version"`
		Data          struct {
			Path   string `json:"artifact_path"`
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.SchemaVersion != "1" || result.Data.Status != "saved" {
		t.Fatalf("bad result %s: %v", out.String(), err)
	}
	shared, _ := connext.SavedLicensePath()
	if result.Data.Path != shared {
		t.Fatal(result.Data.Path)
	}
	old := []byte("old saved license")
	if err := os.WriteFile(shared, old, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err := Execute([]string{"license", "use", source, "--format", "json"}, &out, io.Discard, rt)
	var stderr bytes.Buffer
	if err == nil || ReportError(err, &stderr) == 0 || !json.Valid(stderr.Bytes()) || out.Len() != 0 {
		t.Fatalf("expected structured confirmation error: %v %s", err, stderr.String())
	}
	if got, _ := os.ReadFile(shared); !bytes.Equal(got, old) {
		t.Fatal("replaced without consent")
	}
	if err := Execute([]string{"license", "use", source, "--yes", "--format", "json"}, &out, io.Discard, rt); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{source, shared} {
		if got, _ := os.ReadFile(path); !bytes.Equal(got, content) {
			t.Fatalf("wrong file: %s", path)
		}
	}
}
