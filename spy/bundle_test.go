// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package spy

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

const bundleSpyQoS = `<dds><qos_library name="app_1_base_qos_lib"><qos_profile name="app_1_qos_profile" is_default_qos="true"/></qos_library></dds>`

func TestDownloadBundleQoSForSpy(t *testing.T) {
	app := NewApp(t.TempDir(), io.Discard)
	config := map[string]any{"databus": "db", "templates": map[string]any{"app": "app_1"}}
	calls := 0
	payload := map[string]any{"bundle": map[string]any{"version": float64(2), "qos": bundleSpyQoS}}
	app.APIGet = func(string) (map[string]any, error) { calls++; return payload, nil }
	if err := app.DownloadArtifacts(config, false); err != nil {
		t.Fatal(err)
	}
	qos := filepath.Join(app.AppDir(), "generated", "app_1.qos.xml")
	if got := readFile(t, qos); got != bundleSpyQoS {
		t.Fatalf("QoS=%q", got)
	}
	if err := app.ValidateLocalArtifacts(config); err != nil {
		t.Fatal(err)
	}
	if got := app.qosPath(config); got != qos {
		t.Fatalf("runtime QoS path=%s", got)
	}
	profile, err := QosProfileFromXML(qos, "app_1")
	if err != nil || profile != "app_1_base_qos_lib::app_1_qos_profile" {
		t.Fatalf("profile=%s, error=%v", profile, err)
	}
	if err := app.DownloadArtifacts(config, false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("cached bundle downloaded again")
	}
	// Explicit refresh can return to the legacy server contract.
	payload = map[string]any{"client_config": `<dds><qos_library name="legacy"><qos_profile name="default"/></qos_library></dds>`}
	if err := app.DownloadArtifacts(config, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(qos); !os.IsNotExist(err) {
		t.Fatalf("obsolete bundled QoS remains: %v", err)
	}
	if got := app.qosPath(config); got != filepath.Join(app.AppDir(), "app_1.xml") {
		t.Fatalf("legacy runtime QoS path=%s", got)
	}
}

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
		legacy := filepath.Join(app.AppDir(), "app_1.xml")
		qos := filepath.Join(app.AppDir(), "generated", "app_1.qos.xml")
		for _, path := range []string{legacy, qos} {
			if err := os.WriteFile(path, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		app.APIGet = func(string) (map[string]any, error) {
			return map[string]any{"bundle": bundle, "client_config": "must not fall back"}, nil
		}
		if err := app.DownloadArtifacts(config, true); err == nil {
			t.Fatalf("accepted malformed bundle: %#v", bundle)
		}
		for _, path := range []string{legacy, qos} {
			if got := readFile(t, path); got != "untouched" {
				t.Fatal("invalid response changed existing artifact")
			}
		}
	}
}
