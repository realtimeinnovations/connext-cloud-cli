// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package fileoutput_test

import (
	"errors"
	"fmt"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/app"
	"github.com/realtimeinnovations/connext-cloud-cli/auth"
	"github.com/realtimeinnovations/connext-cloud-cli/config"
	"github.com/realtimeinnovations/connext-cloud-cli/gateway"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
	"github.com/realtimeinnovations/connext-cloud-cli/spy"
)

// Verify root-selection wiring without repeating the utility filesystem matrix.
func TestManagedOutputCallersAcceptSelectedBaseSymlinks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(string) error
	}{
		{"configuration", func(root string) error {
			return config.New(filepath.Join(root, "config.json")).WriteConfig(map[string]string{"region": "test"})
		}},
		{"token", func(root string) error {
			return auth.New(nil, filepath.Join(root, "token.json")).SaveAccessToken("dummy-token", 3600)
		}},
		{"gateway state", func(root string) error {
			return gateway.NewGatewayApp(root, io.Discard).WriteRuntimeState(map[string]any{"test": true})
		}},
		{"spy state", func(root string) error {
			return spy.NewApp(root, io.Discard).WriteRuntimeState(map[string]any{"test": true})
		}},
		{"enrollment", func(root string) error {
			return app.NewRuntime(root, io.Discard).EdgeStore.WriteEnrollArtifacts("service", "0:domain", "participant", "node", edgestore.EnrollArtifacts{DeviceCertPEM: []byte("cert"), PrivateKeyPEM: []byte("key")})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outside := t.TempDir()
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if err := tc.write(filepath.Join(link, "new-root")); err != nil {
				t.Fatal(err)
			}
			written := 0
			if err := filepath.WalkDir(outside, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					data, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					if len(data) == 0 {
						t.Errorf("empty output: %s", path)
					}
					written++
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if written == 0 {
				t.Fatal("no output written through selected base")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 || entries[0].Name() != "new-root" {
				t.Fatalf("output did not reach selected base: %v, %v", entries, err)
			}
		})
	}
}

// Exercise each template caller with one escape, leaving traversal mechanics to
// the utility matrix. Cover both overwrites and missing-parent side effects.
func TestTemplateDownloadsStayWithinTheirOutputDirectory(t *testing.T) {
	for _, kind := range []string{"gateway", "collector", "spy"} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", kind, existing), func(t *testing.T) {
				work := t.TempDir()
				project := "gateway"
				if kind == "spy" {
					project = "spy"
				}
				parent := filepath.Join(work, ".connext", project)
				if err := os.MkdirAll(parent, 0700); err != nil {
					t.Fatal(err)
				}
				extension := ".xml"
				if kind == "spy" {
					parent = filepath.Join(parent, "app")
					extension = ".qos.xml"
					if err := os.MkdirAll(parent, 0700); err != nil {
						t.Fatal(err)
					}
				}
				outside := filepath.Join(parent, "outside"+extension)
				if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
					t.Fatal(err)
				}
				name := "../newdir/outside"
				if existing {
					name = "../outside"
				}
				calls := 0
				get := func(string) (map[string]any, error) {
					calls++
					return map[string]any{"client_config": "replacement", "bundle": map[string]any{"version": 2, "qos": `<dds><qos_library name="lib"><qos_profile name="profile"/></qos_library></dds>`}}, nil
				}
				var err error
				if kind == "spy" {
					app := spy.NewApp(work, io.Discard)
					app.APIGet = get
					err = app.DownloadArtifacts(map[string]any{"databus": "db", "templates": map[string]any{"app": name}}, true)
				} else {
					app := gateway.NewGatewayApp(work, io.Discard)
					app.APIGet = get
					err = app.DownloadArtifacts(map[string]any{"databus": "db", "observability": "obs", "templates": map[string]any{kind: name}}, true)
				}
				if err == nil {
					t.Fatal("accepted template outside output directory")
				}
				if calls != 0 {
					t.Fatalf("invalid destination triggered %d downloads", calls)
				}
				data, err := os.ReadFile(outside)
				if err != nil || string(data) != "untouched" {
					t.Fatalf("outside contents=%q, error=%v", data, err)
				}
				entries, err := os.ReadDir(parent)
				if err != nil || len(entries) != 1 {
					t.Fatalf("rejected output created directories: %v, %v", entries, err)
				}
			})
		}
	}
}

func TestProjectOutputsRejectSymlinksBelowSelectedBase(t *testing.T) {
	for _, kind := range []string{"gateway", "spy", "agent"} {
		t.Run(kind, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			if err := os.Symlink(outside, filepath.Join(root, ".connext")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			runtime := app.NewRuntime(root, io.Discard)
			var err error
			switch kind {
			case "gateway":
				err = runtime.Gateway.WriteConfig(map[string]any{"test": true})
			case "spy":
				err = runtime.Spy.WriteConfig(map[string]any{"test": true})
			case "agent":
				err = runtime.EdgeStore.WriteEnrollArtifacts("service", "domain", "participant", "node", edgestore.EnrollArtifacts{DeviceCertPEM: []byte("cert")})
			}
			if !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("accepted generated link: %v", err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("output escaped: %v, %v", entries, err)
			}
		})
	}
}

func TestRelocatedRTIRootKeepsManagedChildrenStrict(t *testing.T) {
	home, storage, outside := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", home)
	if err := os.Symlink(storage, filepath.Join(home, ".rti")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := config.New("").WriteConfig(map[string]string{"region": "test"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(storage, "rticloud", "configuration", "config.json"))
	if err != nil || !strings.Contains(string(data), "test") {
		t.Fatalf("relocated config=%q, %v", data, err)
	}
	install, err := connext.ManagedInstallationPath()
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(storage)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(install, filepath.Join(canonical, "rticloud", "installations")+string(filepath.Separator)) {
		t.Fatalf("installer did not use relocated storage: %s", install)
	}
	if err := os.Symlink(outside, filepath.Join(storage, "rticloud", "auth")); err != nil {
		t.Fatal(err)
	}
	if err := auth.New(nil, "").SaveAccessToken("dummy", 3600); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("accepted generated auth link: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("token escaped: %v, %v", entries, err)
	}
}
