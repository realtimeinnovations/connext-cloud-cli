// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package common

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSecureFilesValidateWholeResponseBeforeWriting(t *testing.T) {
	for _, bad := range []string{"unsafe-name", "invalid-base64", "invalid-destination"} {
		t.Run(bad, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "credentials")
			files := map[string]string{"client.crt": base64.StdEncoding.EncodeToString([]byte("certificate"))}
			switch bad {
			case "unsafe-name":
				files["../escape"] = files["client.crt"]
			case "invalid-base64":
				files["identity_ca.crt"] = "!bad base64!"
			case "invalid-destination":
				files["identity_ca.crt"] = files["client.crt"]
				if err := os.MkdirAll(filepath.Join(destination, "identity_ca.crt"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := SaveSecureFiles(files, []byte("private key"), destination); err == nil {
				t.Fatal("invalid credential batch accepted")
			}
			for _, path := range []string{filepath.Join(destination, "client.crt"), filepath.Join(destination, "client.key"), filepath.Join(root, "escape")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("partial/escaped output at %s: %v", path, err)
				}
			}
		})
	}
}

func TestSecureFilesPreferLocalKeyAndPreserveExistingPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "client.key")
	if err := os.WriteFile(path, []byte("old key"), 0644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"client.key": "ignored-invalid-server-copy",
		"CLIENT.KEY": "ignored-invalid-uppercase-copy",
		"Client.Key": "ignored-invalid-mixed-case-copy",
		"psk.key":    base64.StdEncoding.EncodeToString([]byte("PSK")),
		"client.crt": base64.StdEncoding.EncodeToString([]byte("certificate")),
	}
	if err := SaveSecureFiles(files, []byte("local private key"), root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 3 {
		t.Fatalf("unexpected credential entries: %v, %v", entries, err)
	}
	for name, want := range map[string]string{"client.key": "local private key", "psk.key": "PSK", "client.crt": "certificate"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s=%q, %v", name, data, err)
		}
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0600)
		if name == "client.crt" || name == "client.key" {
			mode = 0644
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != mode {
			t.Errorf("%s mode=%04o, want=%04o", name, info.Mode().Perm(), mode)
		}
	}
}

func TestSecureFilesKeepServerKeyWhenNoLocalKeySupplied(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"CLIENT.KEY": base64.StdEncoding.EncodeToString([]byte("server key"))}
	if err := SaveSecureFiles(files, nil, root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "CLIENT.KEY"))
	if err != nil || string(data) != "server key" {
		t.Fatalf("server key=%q, %v", data, err)
	}
}
