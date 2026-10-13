// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package spy

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const bundleSpyQoS = `<dds><qos_library name="app_1_base_qos_lib"><qos_profile name="app_1_qos_profile" is_default_qos="true"/></qos_library></dds>`

func TestInvalidBundleLeavesExistingSpyArtifactsUnchanged(t *testing.T) {
	for _, bundle := range []any{
		nil, "invalid", map[string]any{"version": 3, "qos": bundleSpyQoS},
		map[string]any{"version": 2, "qos": " "}, map[string]any{"version": 2, "qos": "<dds>"},
		map[string]any{"version": 2, "qos": "<dds/>"},
	} {
		app := NewApp(t.TempDir(), io.Discard)
		config := map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}}
		if err := os.MkdirAll(filepath.Join(app.AppDir(), "generated"), 0700); err != nil {
			t.Fatal(err)
		}
		qos := filepath.Join(app.AppDir(), "generated", "app_1.qos.xml")
		if err := os.WriteFile(qos, []byte("untouched"), 0600); err != nil {
			t.Fatal(err)
		}
		app.APIGet = func(string) (map[string]any, error) {
			return map[string]any{"bundle": bundle, "client_config": "must not fall back"}, nil
		}
		if err := app.DownloadArtifacts(config, true); err == nil {
			t.Fatalf("accepted malformed bundle: %#v", bundle)
		}
		if got := readFile(t, qos); got != "untouched" {
			t.Fatal("invalid response changed existing artifact")
		}
	}
}

func TestSpyGeneratedPathsRejectSymlinksBeforeSideEffects(t *testing.T) {
	for _, test := range []struct{ name, link, action string }{
		{"QoS file", "generated/app_1.qos.xml", "download"},
		{"generated directory", "generated", "download"},
		{"credential registration", "generated/security", "credentials"},
		{"credential reset", "generated/security", "reset"},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := NewApp(t.TempDir(), io.Discard)
			config := map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}}
			if err := app.WriteConfig(config); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			sentinel := filepath.Join(outside, "client.key")
			if err := os.WriteFile(sentinel, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(app.AppDir(), filepath.FromSlash(test.link))
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			target := outside
			if test.name == "QoS file" {
				target = sentinel
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			app.APIGet = func(string) (map[string]any, error) {
				t.Fatal("unsafe QoS destination triggered a download")
				return nil, nil
			}
			app.GetResourceFunc = func(string) (map[string]any, error) {
				return map[string]any{"secure": true}, nil
			}
			app.GenerateCSRFunc = func(string, string, string) ([]byte, string, error) {
				t.Fatal("unsafe credential destination generated a key")
				return nil, "", nil
			}
			app.APIPost = func(string, map[string]any) (map[string]any, error) {
				t.Fatal("unsafe credential destination registered a client")
				return nil, nil
			}
			var err error
			switch test.action {
			case "download":
				err = app.DownloadArtifacts(config, false)
			case "credentials":
				_, err = app.EnsureSecureArtifacts(config)
			case "reset":
				err = app.Reset()
			}
			if !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("expected path-policy rejection, got %v", err)
			}
			if got := readFile(t, sentinel); got != "untouched" {
				t.Fatal("outside credential changed")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("outside directory changed: %v", err)
			}
			if _, err := os.Stat(app.ConfigPath()); err != nil {
				t.Fatalf("rejected operation removed configuration: %v", err)
			}
		})
	}
}
