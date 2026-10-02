// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationBundleRejectsUnsafeInputs(t *testing.T) {
	artifacts := applicationArtifacts{ClientConfig: "<dds/>", ClientExample: "# example", Manifest: json.RawMessage(`{"topics":[]}`)}
	for field := 0; field < 3; field++ {
		for _, invalid := range []string{"", ".", "..", "../../../../outside", "nested/name", `nested\name`, "/absolute", "C:outside", "bad\x00name"} {
			if field == 2 && invalid == "" {
				continue
			} // Downloads do not have a client ID.
			t.Run(fmt.Sprintf("field%d/%q", field, invalid), func(t *testing.T) {
				names := []string{"db", "app", "client"}
				names[field] = invalid
				bundle, err := applicationBundle(names[0], names[1], names[2], artifacts, nil, nil)
				if err == nil || len(bundle) != 0 {
					t.Fatalf("unsafe input produced %d bytes, error = %v", len(bundle), err)
				}
			})
		}
	}
}

func TestApplicationRejectsUnsafeNamesBeforeRequests(t *testing.T) {
	for _, zipOutput := range []bool{false, true} {
		for field := 0; field < 3; field++ {
			t.Run(fmt.Sprintf("zip%v/field%d", zipOutput, field), func(t *testing.T) {
				api := &fakeAPI{}
				runner := New(api, io.Discard)
				runner.CSRGenerator = func(string, string, string) ([]byte, string, error) {
					t.Fatal("unsafe names must be rejected before generating a key")
					return nil, "", nil
				}
				names := []string{"db", "app", "client"}
				names[field] = "../outside"
				if err := runner.RegisterAppClientWithOptions(names[0], names[1], names[2], "csr", false, false, zipOutput); err == nil {
					t.Fatal("expected invalid-name error")
				}
				if api.lastPath != "" {
					t.Fatalf("unsafe registration made request %q", api.lastPath)
				}
				if field < 2 {
					if err := runner.DownloadApplication(names[0], names[1], ApplicationDownloadOptions{ZIP: zipOutput}); err == nil {
						t.Fatal("expected invalid-name error")
					}
					if api.lastPath != "" {
						t.Fatalf("unsafe download made request %q", api.lastPath)
					}
				}
			})
		}
	}
	t.Chdir(t.TempDir())
	if dir, err := CreateClientBundleDirectory("db", "app", ".."); err == nil {
		t.Fatalf("unsafe client ID created %q", dir)
	}
}

func TestRegistrationPreflightsExistingDestination(t *testing.T) {
	for _, zipOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("zip%v", zipOutput), func(t *testing.T) {
			t.Chdir(t.TempDir())
			destination := "db-app-client"
			if zipOutput {
				destination += ".zip"
				if err := os.WriteFile(destination, []byte("existing archive"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
			api := registrationTestAPI(map[string]any{"client_config": "<dds/>", "client_example": "# example", "manifest": map[string]any{"topics": []any{}}})
			runner := New(api, io.Discard)
			runner.CSRGenerator = func(string, string, string) ([]byte, string, error) {
				t.Fatal("existing output must be checked before generating a key")
				return nil, "", nil
			}
			err := runner.RegisterAppClientWithOptions("db", "app", "client", "", true, false, zipOutput)
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("expected existing-output error, got %v", err)
			}
			if api.lastPath != "" {
				t.Fatalf("existing output caused request %q", api.lastPath)
			}
			if zipOutput {
				data, err := os.ReadFile(destination)
				if err != nil || string(data) != "existing archive" {
					t.Fatalf("existing output changed: %q, %v", data, err)
				}
			}
		})
	}
}

func TestDownloadRejectsMissingRequestedArtifactsBeforeWriting(t *testing.T) {
	for _, field := range []string{"client_config", "client_example", "manifest"} {
		t.Run(field, func(t *testing.T) {
			payload := map[string]any{"client_config": "<dds/>", "client_example": "# example", "manifest": map[string]any{"topics": []any{}}}
			delete(payload, field)
			api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/app": newJSONResponse(http.StatusOK, payload)}}
			runner := New(api, io.Discard)
			runner.WriteFile = func(string, []byte, os.FileMode) error {
				t.Fatal("missing requested artifact must not produce partial output")
				return nil
			}
			options := ApplicationDownloadOptions{GenerateExample: field == "client_example", IncludeManifest: field == "manifest"}
			if err := runner.DownloadApplication("db", "app", options); err == nil {
				t.Fatal("missing requested artifact returned success")
			}
		})
	}
}

func TestWriteOutputFileSecuresExistingTargetBeforeWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "enrollment.key")
	if err := os.WriteFile(path, []byte("old key"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	runner := New(&fakeAPI{}, io.Discard)
	runner.WriteFile = func(name string, data []byte, mode os.FileMode) error {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("target is not private before key write: %v, %v", info, err)
		}
		return os.WriteFile(name, data, mode)
	}
	if err := runner.writeOutputFile(path, []byte("new key"), true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new key" {
		t.Fatalf("key = %q, error = %v", data, err)
	}
}

func TestWriteOutputFileDoesNotWriteIfPreChmodFails(t *testing.T) {
	runner := New(&fakeAPI{}, io.Discard)
	runner.Chmod = func(string, os.FileMode) error { return os.ErrPermission }
	runner.WriteFile = func(string, []byte, os.FileMode) error {
		t.Fatal("must not write key after permission failure")
		return nil
	}
	if err := runner.writeOutputFile("enrollment.key", []byte("new key"), true); err == nil {
		t.Fatal("expected permission error")
	}
}

func TestDownloadPreflightsAllRequestedDestinations(t *testing.T) {
	for _, existing := range []string{"app.xml", "app.py", "manifest.json"} {
		t.Run(existing, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, existing), []byte("old content"), 0o644); err != nil {
				t.Fatal(err)
			}
			api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/app": newJSONResponse(http.StatusOK, map[string]any{
				"client_config": "<new/>", "client_example": "# new example", "manifest": map[string]any{"version": 1},
			})}}
			runner := New(api, io.Discard)
			err := runner.DownloadApplication("db", "app", ApplicationDownloadOptions{TargetDir: dir, GenerateExample: true, IncludeManifest: true})
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("expected existing-output error, got %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != existing {
				t.Fatalf("download produced partial output: %v, %v", entries, err)
			}
			data, err := os.ReadFile(filepath.Join(dir, existing))
			if err != nil || string(data) != "old content" {
				t.Fatalf("existing file changed: %q, %v", data, err)
			}
		})
	}
}

func TestDownloadForceOverwritesAllRequestedArtifacts(t *testing.T) {
	dir := t.TempDir()
	want := map[string]string{"app.xml": "<new/>", "app.py": "# new example", "manifest.json": "{\n  \"version\": 1\n}\n"}
	for name := range want {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/app": newJSONResponse(http.StatusOK, map[string]any{
		"client_config": "<new/>", "client_example": "# new example", "manifest": map[string]any{"version": 1},
	})}}
	runner := New(api, io.Discard)
	if err := runner.DownloadApplication("db", "app", ApplicationDownloadOptions{TargetDir: dir, GenerateExample: true, IncludeManifest: true, ForceOverwrite: true}); err != nil {
		t.Fatal(err)
	}
	for name, content := range want {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != content {
			t.Fatalf("%s = %q, error = %v; want %q", name, data, err, content)
		}
	}
}

func TestDownloadPreflightRejectsInaccessibleDestination(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/app": newJSONResponse(http.StatusOK, map[string]any{
		"client_config": "<new/>", "manifest": map[string]any{"version": 1},
	})}}
	runner := New(api, io.Discard)
	runner.Stat = func(name string) (os.FileInfo, error) {
		if filepath.Base(name) == "manifest.json" {
			return nil, os.ErrPermission
		}
		return nil, os.ErrNotExist
	}
	runner.WriteFile = func(string, []byte, os.FileMode) error {
		t.Fatal("must inspect all destinations before writing")
		return nil
	}
	if err := runner.DownloadApplication("db", "app", ApplicationDownloadOptions{IncludeManifest: true}); err == nil {
		t.Fatal("expected preflight error")
	}
}
