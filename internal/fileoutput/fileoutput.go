// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

// Package fileoutput owns destination validation for CLI-managed output.
// Existing ancestors must be directories, and outputs must be regular files.
// Symlinks, including dangling links and links above a caller's selected root,
// are rejected. Missing paths are permitted during preflight.
//
// Writes stage a complete file beside the destination before replacing it.
// This avoids truncation on copy failure and writing through existing hard links.
// Validation is path-based: it does not protect against concurrent replacement of
// ancestor directories. Callers needing a multi-file preflight must still run it
// before side effects; individual writes are not a multi-file transaction.
package fileoutput

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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

// absolutePath expands only macOS's fixed system aliases. Arbitrary user links
// must never be resolved before validation, since that would hide an escape.
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
				return nil, &os.PathError{Op: "validate output", Path: current, Err: syscall.ENOTDIR}
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

// WriteFile replaces or creates a regular file. Callers own whether replacement
// is appropriate (force, generated refresh, or editable-example preservation).
// Private modes apply before publication, including when replacing public files.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	return Copy(path, bytes.NewReader(data), mode)
}

// Copy is the streaming form of WriteFile. Failed reads leave the old file intact.
func Copy(path string, source io.Reader, mode os.FileMode) error {
	var err error
	path, err = absolutePath(path)
	if err != nil {
		return err
	}
	info, err := Inspect(path, File)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	// Never loosen an existing mode.
	if info != nil {
		mode &= info.Mode().Perm()
	}
	directory := filepath.Dir(path)
	directoryMode := os.FileMode(0o755)
	if mode.Perm()&0o077 == 0 {
		directoryMode = 0o700
	}
	if err := MkdirAll(directory, directoryMode); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".rticloud-output-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := io.Copy(temporary, source); err != nil {
		return err
	}
	if err := temporary.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := Validate(path, File); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

// OpenAppend opens a validated regular file for a long-lived append stream.
// Unlike replacement writes, append streams necessarily retain the existing inode.
func OpenAppend(path string, mode os.FileMode) (*os.File, error) {
	var err error
	path, err = absolutePath(path)
	if err != nil {
		return nil, err
	}
	if err := Validate(path, File); err != nil {
		return nil, err
	}
	if err := MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
}
