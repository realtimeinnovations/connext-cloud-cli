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
