// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package rtipaths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMigrationMovesKnownFilesAndPreservesConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "move"
		if conflict {
			name = "conflict"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			legacy, root := filepath.Join(home, ".rticloud"), filepath.Join(home, ".rti", "rticloud")
			if err := os.MkdirAll(legacy, 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"config.json": "configuration", "credentials.json": "auth", "workspaces_credentials.json": "auth"}
			for file, directory := range files {
				if err := os.WriteFile(filepath.Join(legacy, file), []byte("old"), 0o644); err != nil {
					t.Fatal(err)
				}
				if conflict {
					if err := os.MkdirAll(filepath.Join(root, directory), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, directory, file), []byte("current"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := os.WriteFile(filepath.Join(legacy, "unrelated"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := migrateLegacy(legacy, root); err != nil {
				t.Fatal(err)
			}
			for file, directory := range files {
				target := filepath.Join(root, directory, file)
				data, err := os.ReadFile(target)
				want := "old"
				if conflict {
					want = "current"
				}
				if err != nil || string(data) != want {
					t.Fatalf("%s: incorrect migrated content: %v", file, err)
				}
				info, err := os.Stat(target)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
					t.Fatal("file permissions are not private")
				}
				_, err = os.Stat(filepath.Join(legacy, file))
				if !conflict && !os.IsNotExist(err) {
					t.Fatal("legacy source was not removed")
				}
				if conflict && err != nil {
					t.Fatal("conflicting source was removed")
				}
				// Simulate logout/removal: subsequent migration must not resurrect old data.
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
			if err := migrateLegacy(legacy, root); err != nil {
				t.Fatal(err)
			}
			for file, directory := range files {
				if _, err := os.Stat(filepath.Join(root, directory, file)); !os.IsNotExist(err) {
					t.Fatal("legacy data resurrected")
				}
			}
			if _, err := os.Stat(filepath.Join(legacy, "unrelated")); err != nil {
				t.Fatal("unrelated file removed")
			}
		})
	}
}

func TestMigrationFailurePreservesSourceAndCanRetry(t *testing.T) {
	home := t.TempDir()
	legacy, root := filepath.Join(home, "legacy"), filepath.Join(home, "root")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacy(legacy, root); err == nil {
		t.Fatal("expected migration error")
	}
	if _, err := os.Stat(filepath.Join(legacy, "config.json")); err != nil {
		t.Fatal("source lost after failure")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := migrateLegacy(legacy, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatal("empty legacy directory not removed")
	}
}

func TestNoLegacyDirectoryDoesNotCreateState(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "root")
	if err := migrateLegacy(filepath.Join(home, "missing"), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("unnecessary state created")
	}
}
