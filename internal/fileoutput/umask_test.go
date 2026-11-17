// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

//go:build darwin || linux

package fileoutput

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCreationRespectsUmask(t *testing.T) {
	// Umask is process-global; isolate it from other tests and goroutines.
	if os.Getenv("RTICLOUD_TEST_UMASK") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestCreationRespectsUmask$")
		cmd.Env = append(os.Environ(), "RTICLOUD_TEST_UMASK=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("umask subprocess: %v\n%s", err, output)
		}
		return
	}
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)
	root := t.TempDir()
	for _, kind := range []string{"bytes", "stream"} {
		path := filepath.Join(root, kind)
		var err error
		if kind == "bytes" {
			err = WriteFile(path, []byte("data"), 0644)
		} else {
			err = Copy(path, strings.NewReader("data"), 0644)
		}
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("%s mode=%o, want 600 with umask 077", kind, info.Mode().Perm())
		}
	}
}
