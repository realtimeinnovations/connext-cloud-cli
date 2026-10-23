// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

// Package fileoutput owns destination validation for CLI-managed output.
// Existing ancestors must be directories, and outputs must be regular files.
// Callers resolve user-selected directories with ResolveDirectory before joining
// generated paths. Validation rejects remaining symlinks. Missing paths are
// permitted during preflight.
//
// Writers validate, then use ordinary OS writes without changing their semantics.
// Validation is path-based: it does not protect against concurrent replacement of
// ancestor directories. Callers needing a multi-file preflight must still run it
// before side effects; individual writes are not a multi-file transaction.
package fileoutput

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Kind uint8

const (
	File Kind = iota
	Directory
	FileOrDirectory
)

// ValidName rejects path syntax in a single name, including API-supplied names.
func ValidName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\:\x00")
}

// absolutePath expands only macOS's fixed system aliases. Strict operations
// must not resolve arbitrary links, since that would hide generated-path escapes.
func absolutePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("output path is empty")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/var", "/tmp", "/etc"} {
			if path == alias || strings.HasPrefix(path, alias+"/") {
				target, err := os.Readlink(alias)
				if err == nil && (target == "private"+alias || target == "/private"+alias) {
					path = "/private" + path
				}
				break
			}
		}
	}
	return path, nil
}

// ResolveDirectory resolves links in a user-selected base directory, including
// its ancestors. Missing trailing directories are preserved without creating them.
// Call this before joining generated paths, never on generated subdirectories:
// writers must still reject links below the selected base.
func ResolveDirectory(path string) (string, error) {
	absolute, err := absolutePath(path)
	if err != nil {
		return "", err
	}
	current := absolute
	var missing []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			// Lstat distinguishes a dangling link from a missing directory.
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			if err := Validate(resolved, Directory); err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(current) == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = filepath.Dir(current)
	}
}

// ResolveFile resolves the parent of an explicitly selected output file, while
// leaving the file itself subject to strict validation (including link rejection).
func ResolveFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("output path is empty")
	}
	parent, err := ResolveDirectory(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	resolved := filepath.Join(parent, filepath.Base(path))
	return resolved, nil
}

// Inspect returns the destination's metadata after checking all ancestors.
// Missing destinations return an error matching os.ErrNotExist, like os.Stat.
func Inspect(path string, kind Kind) (os.FileInfo, error) {
	if kind > FileOrDirectory {
		return nil, fmt.Errorf("invalid output kind: %d", kind)
	}
	absolute, err := absolutePath(path)
	if err != nil {
		return nil, err
	}
	// Walk from the volume root downward so an intermediate symlink is rejected
	// before inspecting anything beneath it.
	var paths []string
	for current := absolute; ; current = filepath.Dir(current) {
		paths = append(paths, current)
		if filepath.Dir(current) == current {
			break
		}
	}
	var info os.FileInfo
	for i := len(paths) - 1; i >= 0; i-- {
		current := paths[i]
		info, err = os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, &os.PathError{Op: "validate output", Path: current, Err: fmt.Errorf("symlinks are not allowed: %w", os.ErrInvalid)}
		}
		if i > 0 || kind == Directory {
			if !info.IsDir() {
				// Windows treats ENOTDIR as not-exist. This is a policy
				// rejection, never a missing path permitted by preflight.
				return nil, &os.PathError{Op: "validate output", Path: current, Err: fmt.Errorf("not a directory: %w", os.ErrInvalid)}
			}
		} else if !info.Mode().IsRegular() && !(kind == FileOrDirectory && info.IsDir()) {
			return nil, &os.PathError{Op: "validate output", Path: current, Err: fmt.Errorf("not a regular file: %w", os.ErrInvalid)}
		}
	}
	return info, nil
}

func StatFile(path string) (os.FileInfo, error) { return Inspect(path, File) }

func Validate(path string, kind Kind) error {
	_, err := Inspect(path, kind)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// ValidateWithin additionally checks lexical containment of a generated path.
// root is a containment boundary, not a trust boundary: its ancestors are checked.
func ValidateWithin(root, target string, kind Kind) error {
	root, err := absolutePath(root)
	if err != nil {
		return err
	}
	target, err = absolutePath(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return fmt.Errorf("output path is outside root: %s", target)
	}
	return Validate(target, kind)
}

// MkdirAll validates before creation; existing directory permissions are retained.
func MkdirAll(path string, mode os.FileMode) error {
	var err error
	path, err = absolutePath(path)
	if err != nil {
		return err
	}
	if err := Validate(path, Directory); err != nil {
		return err
	}
	return os.MkdirAll(path, mode)
}

// WriteFile validates the destination, then uses os.WriteFile unchanged.
// Callers retain parent creation, permissions, and overwrite policy.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	path, err := validatedFile(path)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, mode)
}

// Copy validates before opening a stream with ordinary truncate/write semantics.
// As with an ordinary write, a copy failure can leave partial output.
func Copy(path string, source io.Reader, mode os.FileMode) error {
	file, err := OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// OpenFile validates a regular-file destination before applying the caller's flags.
func OpenFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	path, err := validatedFile(path)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(path, flags, mode)
}

func validatedFile(path string) (string, error) {
	path, err := absolutePath(path)
	if err != nil {
		return "", err
	}
	return path, Validate(path, File)
}

// OpenAppend is OpenFile with the standard log append flags.
func OpenAppend(path string, mode os.FileMode) (*os.File, error) {
	return OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
}
