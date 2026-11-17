// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package fileoutput

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// Record observable state without following links or reading special files.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += string(data)
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += target
		}
		entries[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestRejectedDestinationsLeaveFilesystemUnchanged(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling-symlink", "parent-symlink", "ancestor-symlink", "dangling-parent", "directory", "file-parent", "socket"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			outside := filepath.Join(root, "outside")
			if err := os.Mkdir(outside, 0700); err != nil {
				t.Fatal(err)
			}
			external := filepath.Join(outside, "file")
			if err := os.WriteFile(external, []byte("untouched"), 0644); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "output")
			link, linkTarget := target, external
			switch kind {
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "file-parent":
				if err := os.WriteFile(target, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(target, "child")
			case "socket":
				// Keep the socket pathname short on macOS.
				t.Chdir(root)
				listener, err := net.Listen("unix", "output")
				if err != nil {
					t.Skipf("Unix sockets unavailable: %v", err)
				}
				defer listener.Close()
			default:
				switch kind {
				case "dangling-symlink":
					linkTarget = filepath.Join(outside, "missing")
				case "parent-symlink":
					linkTarget = outside
					target = filepath.Join(link, "file")
				case "ancestor-symlink":
					linkTarget = outside
					target = filepath.Join(link, "new", "nested", "file")
				case "dangling-parent":
					linkTarget = filepath.Join(outside, "missing")
					target = filepath.Join(link, "child")
				}
				if err := os.Symlink(linkTarget, link); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			before := snapshot(t, root)
			if err := Validate(target, File); err == nil {
				t.Error("preflight accepted invalid output")
			}
			if err := WriteFile(target, []byte("must not escape"), 0600); err == nil {
				t.Error("write accepted invalid output")
			}
			if stream, err := OpenAppend(target, 0o644); err == nil {
				stream.Close()
				t.Error("append accepted invalid output")
			}
			if after := snapshot(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected write changed filesystem: before=%v after=%v", before, after)
			}
		})
	}
}

func TestContainmentAndDirectoryPreflight(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing", "file")
	if err := ValidateWithin(root, missing, File); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("preflight created directory: %v", err)
	}
	for _, target := range []string{filepath.Join(root, "..", "escaped"), root + "-sibling/file"} {
		if err := ValidateWithin(root, target, File); err == nil {
			t.Errorf("accepted escaped target %s", target)
		}
	}
	t.Chdir(root)
	if err := ValidateWithin(".", missing, File); err != nil {
		t.Fatalf("mixed absolute/relative paths: %v", err)
	}
	if err := WriteFile("file", []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Validate("file", Directory); err == nil {
		t.Fatal("directory preflight accepted file")
	}
	if err := MkdirAll("file/child", 0700); err == nil {
		t.Fatal("directory creation accepted file parent")
	}
}

// Holding the file open verifies in-place writing, including Windows handles
// that allow writes but deny replacement. Existing permission bits must survive.
func TestOverwritePreservesOpenFileAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, []byte("old contents longer than new"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("new contents"), 0644); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "new contents" {
		t.Fatalf("open file contents=%q, error=%v", data, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Mode() != after.Mode() {
		t.Fatalf("permissions changed: %v -> %v", before.Mode(), after.Mode())
	}
}

type failedReader struct{ err error }

func (r failedReader) Read(p []byte) (int, error) { return copy(p, "partial"), r.err }

func TestCopyReportsReadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output")
	failure := errors.New("download interrupted")
	if err := Copy(path, failedReader{failure}, 0600); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
}

func TestWriterLeavesParentCreationToCaller(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "missing", "output")
	if err := WriteFile(path, []byte("data"), 0600); !os.IsNotExist(err) {
		t.Fatalf("want missing-parent error, got %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("write created parent: %v, %v", entries, err)
	}
}

func TestInspectionErrorsAreNotMissingPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0700)
	path := filepath.Join(locked, "file")
	if _, err := os.Lstat(path); !os.IsPermission(err) {
		t.Skip("process can bypass directory permissions")
	}
	if err := WriteFile(path, []byte("data"), 0600); !os.IsPermission(err) {
		t.Fatalf("permission error lost: %v", err)
	}
}

func TestSystemTempAliasAndRelativePaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := WriteFile("nested"+string(filepath.Separator)+".."+string(filepath.Separator)+"relative", []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "absolute")
	if runtime.GOOS == "darwin" {
		path = strings.TrimPrefix(path, "/private")
	}
	if err := WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
}

// A destination may change after a command's preflight. The writer must check it
// again instead of trusting that earlier result.
func TestWriteRechecksAfterPreflight(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "output")
	if err := Validate(path, File); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := Copy(path, strings.NewReader("data"), 0600); err == nil {
		t.Fatal("writer trusted stale preflight")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("directory changed: %v, %v", entries, err)
	}
}

func TestAppendPreservesPreviousLogContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.log")
	for _, line := range []string{"first\n", "second\n"} {
		file, err := OpenAppend(path, 0644)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.WriteString(line)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("append: %v, close: %v", writeErr, closeErr)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first\nsecond\n" {
		t.Fatalf("log=%q, %v", data, err)
	}
}

func TestResolveDirectorySelectsBaseWithoutCreatingIt(t *testing.T) {
	base := t.TempDir()
	link := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(base, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, suffix := range []string{"", "new/nested"} {
		selected := filepath.Join(link, suffix)
		resolved, err := ResolveDirectory(selected)
		if err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, base)
		if len(before) != 1 {
			t.Fatalf("resolution created output: %v", before)
		}
		if err := MkdirAll(resolved, 0700); err != nil {
			t.Fatal(err)
		}
		if err := WriteFile(filepath.Join(resolved, "file"), []byte("through selected root"), 0600); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(base, suffix, "file"))
		if err != nil || string(data) != "through selected root" {
			t.Fatalf("output=%q, %v", data, err)
		}
		if err := os.RemoveAll(filepath.Join(base, "new")); err != nil {
			t.Fatal(err)
		}
		if suffix == "" {
			if err := os.Remove(filepath.Join(base, "file")); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResolveDirectoryRejectsInvalidBase(t *testing.T) {
	for _, kind := range []string{"dangling", "cycle", "file"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			path := filepath.Join(base, "selected")
			if kind == "file" {
				if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				target := path
				if kind == "dangling" {
					target = filepath.Join(base, "missing")
				}
				if err := os.Symlink(target, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			before := snapshot(t, base)
			for _, selected := range []string{path, filepath.Join(path, "new")} {
				if resolved, err := ResolveDirectory(selected); err == nil {
					t.Fatalf("accepted invalid base %s as %s", selected, resolved)
				}
			}
			if after := snapshot(t, base); !reflect.DeepEqual(before, after) {
				t.Fatalf("resolution mutated files: %v", after)
			}
		})
	}
}

func TestResolvedBaseStillRejectsGeneratedSymlinks(t *testing.T) {
	base, outside := t.TempDir(), t.TempDir()
	selected := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(base, selected); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolved, err := ResolveDirectory(selected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "generated")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(resolved, "generated", "secret"), []byte("secret"), 0600); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("write error=%v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("write escaped: %v, %v", entries, err)
	}
	// An explicit file may select its parent, never the file symlink itself.
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(base, "file")); err != nil {
		t.Fatal(err)
	}
	file, err := ResolveFile(filepath.Join(selected, "file"))
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(file, []byte("secret"), 0600); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("file link accepted: %v", err)
	}
}

func TestResolveFileDoesNotDuplicateTrailingDirectoryName(t *testing.T) {
	root := t.TempDir()
	for _, suffix := range []string{"existing", "new"} {
		directory := filepath.Join(root, suffix)
		if suffix == "existing" {
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
		}
		resolved, err := ResolveFile(directory + string(filepath.Separator))
		if err != nil {
			t.Fatal(err)
		}
		err = WriteFile(resolved, []byte("file"), 0600)
		if suffix == "existing" {
			if !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("accepted existing directory: %v", err)
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatalf("nested file created: %v, %v", entries, err)
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(directory)
			if err != nil || string(data) != "file" {
				t.Fatalf("wrong output: %q, %v", data, err)
			}
		}
	}
}
