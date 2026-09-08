// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package connext

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPruneManagedInstallationPreservesRuntime(t *testing.T) {
	root := managedTestEnvironment(t)
	directory := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
	removed := []string{"doc", "lib", "resource/app/eclipse", "resource/app/node-18", "resource/app/node-20", "resource/app/app_support/system_designer", "resource/app/app_support/admin_console"}
	preserved := []string{"bin", "third_party", "resource/scripts", "resource/app/node", "resource/app/other", "resource/app/app_support/other"}
	for _, name := range append(append([]string{}, removed...), preserved...) {
		path := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "keep-or-remove"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "rti_license.dat"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if warnings := pruneManagedInstallation(directory, DiscoveryOptions{}); len(warnings) != 0 {
			t.Fatal(warnings)
		}
	}
	for _, name := range removed {
		if _, err := os.Stat(filepath.Join(directory, filepath.FromSlash(name))); !os.IsNotExist(err) {
			t.Fatalf("not removed: %s", name)
		}
	}
	for _, name := range append(preserved, "rti_license.dat", "rti_versions.xml") {
		if _, err := os.Stat(filepath.Join(directory, filepath.FromSlash(name))); err != nil {
			t.Fatalf("runtime content lost: %s", name)
		}
	}
}

func TestPruneRejectsExternalAndInvalidInstallations(t *testing.T) {
	root := managedTestEnvironment(t)
	external := writeManagedFixture(t, t.TempDir(), installerVersion)
	if warnings := pruneManagedInstallation(external, DiscoveryOptions{}); len(warnings) == 0 {
		t.Fatal("external installation accepted")
	}
	directory := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), "7.6.0.1")
	if err := os.Mkdir(filepath.Join(directory, "doc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if warnings := pruneManagedInstallation(directory, DiscoveryOptions{}); len(warnings) == 0 {
		t.Fatal("invalid installation accepted")
	}
	if _, err := os.Stat(filepath.Join(directory, "doc")); err != nil {
		t.Fatal("invalid installation modified")
	}
}

func TestPruneDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires symlink privileges")
	}
	for _, name := range []string{"doc", "resource"} {
		t.Run(name, func(t *testing.T) {
			root := managedTestEnvironment(t)
			directory := writeManagedFixture(t, filepath.Join(root, installerVersion, platformDirectory()), installerVersion)
			outside := t.TempDir()
			if err := os.MkdirAll(filepath.Join(outside, "app", "eclipse"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(directory, name)); err != nil {
				t.Fatal(err)
			}
			if warnings := pruneManagedInstallation(directory, DiscoveryOptions{}); len(warnings) == 0 {
				t.Fatal("expected symlink warning")
			}
			if _, err := os.Stat(filepath.Join(outside, "app", "eclipse")); err != nil {
				t.Fatal("followed symlink")
			}
		})
	}
}
