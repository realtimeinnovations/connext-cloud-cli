// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package rtipaths

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var migrationMu sync.Mutex

// MigrateLegacy moves only the three known user files. A completion marker
// prevents retained legacy credentials from being restored after logout.
func MigrateLegacy() error {
	home, root, err := resolveHomeAndCloudRoot()
	if err != nil {
		return err
	}
	migrationMu.Lock()
	defer migrationMu.Unlock()
	return migrateLegacy(filepath.Join(home, ".rticloud"), root)
}

func migrateLegacy(legacy, root string) error {
	marker := filepath.Join(root, "configuration", ".legacy-migrated")
	if _, err := os.Lstat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(legacy); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	for _, file := range []struct{ name, directory string }{
		{"config.json", "configuration"},
		{"credentials.json", "auth"},
		{"workspaces_credentials.json", "auth"},
	} {
		if file.directory == "auth" {
			if _, err := os.Lstat(credentialMigrationMarker(root, file.name)); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		source := filepath.Join(legacy, file.name)
		target := filepath.Join(root, file.directory, file.name)
		if err := migrateFile(source, target); err != nil {
			return fmt.Errorf("migrate legacy CLI file %s: %w", source, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(marker, []byte("Legacy migration complete; existing destination files took precedence.\n"), 0o600); err != nil {
		return err
	}
	// Remove only an empty legacy directory; unknown files and conflicts remain.
	_ = os.Remove(legacy)
	return nil
}

func migrateFile(source, target string) error {
	info, err := os.Lstat(source)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("legacy file must be a regular file")
	}
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(target), ".migration-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := io.Copy(temporary, input); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// Publish a complete private file without overwriting a concurrent writer.
	// The temporary file and destination reside on the same filesystem.
	if err := os.Link(temporary.Name(), target); err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	// Windows cannot remove an open source file.
	if err := input.Close(); err != nil {
		return err
	}
	return os.Remove(source)
}

func credentialMigrationMarker(root, name string) string {
	return filepath.Join(root, "auth", "."+name+".legacy-disabled")
}

// ClearCredentials must work even if migration of unrelated legacy files fails.
// The per-credential marker also prevents a retained legacy token from returning.
func ClearCredentials(target string) error {
	home, root, err := resolveHomeAndCloudRoot()
	if err != nil {
		return err
	}
	migrationMu.Lock()
	defer migrationMu.Unlock()
	name := filepath.Base(target)
	if target != filepath.Join(root, "auth", name) || (name != "credentials.json" && name != "workspaces_credentials.json") {
		return fmt.Errorf("not a default credential path: %s", target)
	}
	var errs []error
	marker := credentialMigrationMarker(root, name)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		errs = append(errs, err)
	} else if err := os.WriteFile(marker, []byte("Legacy credential migration disabled by logout.\n"), 0o600); err != nil {
		errs = append(errs, err)
	}
	paths := []string{target, filepath.Join(home, ".rticloud", name)}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
