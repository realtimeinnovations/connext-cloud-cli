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
	"net"
	"os"
	"path/filepath"
	"testing"
)

func splitArtifacts() applicationArtifacts {
	return applicationArtifacts{
		Manifest: json.RawMessage(`{"version":1,"topics":[]}`),
		Bundle:   &applicationBundleV2{Version: 2, QoS: "<generated-qos/>", Types: "<types/>", Application: "<editable-app/>", Example: "# editable example", Readme: "# Instructions"},
	}
}

func splitAPI() *fakeAPI {
	data, _ := json.Marshal(splitArtifacts())
	var payload map[string]any
	_ = json.Unmarshal(data, &payload)
	return registrationTestAPI(payload)
}

func TestSplitRefreshPreservesExamplesAndSecurity(t *testing.T) {
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprint(force), func(t *testing.T) {
			dir := t.TempDir()
			original := map[string]string{"app.xml": "custom XML", "app.py": "custom Python", "README.md": "custom instructions", "generated/security/client.key": "private key", "generated/security/psk.key": "PSK", "generated/app.qos.xml": "old qos", "generated/app.types.xml": "old types"}
			for name, content := range original {
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runner := New(splitAPI(), io.Discard)
			if err := runner.DownloadApplication("db", "app", ApplicationDownloadOptions{TargetDir: dir, GenerateExample: true, ForceOverwrite: force}); err != nil {
				t.Fatal(err)
			}
			expected := map[string]string{"generated/app.qos.xml": "<generated-qos/>", "generated/app.types.xml": "<types/>"}
			for name, content := range original {
				if _, ok := expected[name]; !ok {
					expected[name] = content
				}
			}
			if force {
				expected["app.xml"] = "<editable-app/>"
				expected["app.py"] = "# editable example"
				expected["README.md"] = "# Instructions"
			}
			for name, want := range expected {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s = %q (%v), want %q", name, data, err, want)
				}
			}
			info, err := os.Stat(filepath.Join(dir, "generated/security/client.key"))
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("key permissions changed: %v", err)
			}
		})
	}
}

func TestSplitDownloadSelection(t *testing.T) {
	cases := []struct {
		name    string
		options ApplicationDownloadOptions
		want    []string
	}{
		{"default", ApplicationDownloadOptions{}, []string{"generated/app.qos.xml", "generated/app.types.xml"}},
		{"generated", ApplicationDownloadOptions{Generated: true}, []string{"generated/app.qos.xml", "generated/app.types.xml"}},
		{"qos", ApplicationDownloadOptions{QoS: true}, []string{"generated/app.qos.xml"}},
		{"types", ApplicationDownloadOptions{Types: true}, []string{"generated/app.types.xml"}},
		{"example", ApplicationDownloadOptions{GenerateExample: true}, []string{"generated/app.qos.xml", "generated/app.types.xml", "app.xml", "app.py", "README.md", "manifest.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.options.TargetDir = dir
			if err := New(splitAPI(), io.Discard).DownloadApplication("db", "app", tc.options); err != nil {
				t.Fatal(err)
			}
			files := map[string]bool{}
			err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					name, _ := filepath.Rel(dir, path)
					files[filepath.ToSlash(name)] = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != len(tc.want) {
				t.Fatalf("unexpected files: %v", files)
			}
			for _, name := range tc.want {
				if !files[name] {
					t.Fatalf("missing %s", name)
				}
			}
		})
	}
}

func TestInvalidSplitBundleFailsBeforeRegistration(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, field := range []string{"qos", "types", "application", "example", "readme", "version"} {
		t.Run(field, func(t *testing.T) {
			data, _ := json.Marshal(splitArtifacts())
			var payload map[string]any
			_ = json.Unmarshal(data, &payload)
			delete(payload["bundle"].(map[string]any), field)
			api := &registrationOrderAPI{fakeAPI: registrationTestAPI(payload)}
			runner := New(api, io.Discard)
			runner.CSRGenerator = func(string, string, string) ([]byte, string, error) {
				t.Fatal("invalid bundle must fail before key generation")
				return nil, "", nil
			}
			if err := runner.RegisterAppClientWithOptions("db", "app", "client", ApplicationRegistrationOptions{GeneratePrivateKey: true}); err == nil {
				t.Fatal("expected invalid bundle error")
			}
			if api.lastPayload != nil {
				t.Fatal("invalid artifacts must not register a client")
			}
		})
	}
}

func testApplicationBundle(application, example string) *applicationBundleV2 {
	return &applicationBundleV2{Version: 2, QoS: "<dds/>", Types: "<types/>", Application: application, Example: example, Readme: "# Instructions"}
}

func TestCloudApplicationRejectsFlatResponseBeforeWritingOrRegistering(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, register := range []bool{false, true} {
		api := &registrationOrderAPI{fakeAPI: registrationTestAPI(map[string]any{
			"client_config": "<dds/>", "client_example": "# old", "manifest": map[string]any{"topics": []any{}},
		})}
		runner := New(api, io.Discard)
		runner.WriteFile = func(string, []byte, os.FileMode) error {
			t.Fatal("must reject obsolete response before writing")
			return nil
		}
		runner.CSRGenerator = func(string, string, string) ([]byte, string, error) {
			t.Fatal("must reject obsolete response before creating credentials")
			return nil, "", nil
		}
		var err error
		if register {
			err = runner.RegisterAppClientWithOptions("db", "app", "client", ApplicationRegistrationOptions{GeneratePrivateKey: true})
		} else {
			err = runner.DownloadApplication("db", "app", ApplicationDownloadOptions{GenerateExample: true})
		}
		if err == nil {
			t.Fatal("expected missing bundle error")
		}
		if api.lastPayload != nil {
			t.Fatal("obsolete artifacts must not register a client")
		}
	}
}

func TestSplitRefreshRejectsSymlinkedDestinations(t *testing.T) {
	for _, kind := range []string{"file", "dangling-file", "generated-directory", "bundle-directory"} {
		t.Run(kind, func(t *testing.T) {
			outside := t.TempDir()
			external := filepath.Join(outside, "app.types.xml")
			if kind != "dangling-file" {
				if err := os.WriteFile(external, []byte("external content"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			bundle := filepath.Join(t.TempDir(), "bundle")
			generated := filepath.Join(bundle, "generated")
			link, target := filepath.Join(generated, "app.types.xml"), external
			if kind == "bundle-directory" {
				generated = filepath.Join(outside, "generated")
				if err := os.Mkdir(generated, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(external, filepath.Join(generated, "app.types.xml")); err != nil {
					t.Fatal(err)
				}
				external = filepath.Join(generated, "app.types.xml")
				link, target = bundle, outside
			} else if kind == "generated-directory" {
				if err := os.Mkdir(bundle, 0o700); err != nil {
					t.Fatal(err)
				}
				link, target = generated, outside
				generated = outside
			} else if err := os.MkdirAll(generated, 0o700); err != nil {
				t.Fatal(err)
			}
			qos := filepath.Join(generated, "app.qos.xml")
			if err := os.WriteFile(qos, []byte("existing qos"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}

			err := New(splitAPI(), io.Discard).DownloadApplication("db", "app", ApplicationDownloadOptions{TargetDir: bundle})
			if err == nil {
				t.Error("refresh accepted a symlinked destination")
			}
			if data, err := os.ReadFile(qos); err != nil || string(data) != "existing qos" {
				t.Errorf("refresh changed an earlier output before rejecting the symlink: %q, %v", data, err)
			}
			data, err := os.ReadFile(external)
			if kind == "dangling-file" {
				if !os.IsNotExist(err) {
					t.Errorf("refresh created an external symlink target: %q, %v", data, err)
				}
			} else if err != nil || string(data) != "external content" {
				t.Errorf("refresh changed the external file: %q, %v", data, err)
			}
		})
	}
}

func TestZIPDownloadRejectsInvalidDestinations(t *testing.T) {
	for _, kind := range []string{"archive-symlink", "dangling-archive", "target-directory-symlink", "archive-directory"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%v", kind, force), func(t *testing.T) {
				dir, outside := t.TempDir(), t.TempDir()
				external := filepath.Join(outside, "db-app.zip")
				if kind != "dangling-archive" {
					if err := os.WriteFile(external, []byte("external content"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				targetDir := dir
				link, target := filepath.Join(dir, "db-app.zip"), external
				if kind == "target-directory-symlink" {
					targetDir = filepath.Join(dir, "bundle")
					link, target = targetDir, outside
				}
				if kind == "archive-directory" {
					if err := os.Mkdir(link, 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlink creation unavailable: %v", err)
				}
				runner := New(splitAPI(), io.Discard)
				if err := runner.DownloadApplication("db", "app", ApplicationDownloadOptions{TargetDir: targetDir, ZIP: true, ForceOverwrite: force}); err == nil {
					t.Error("ZIP download accepted an invalid destination")
				}
				data, err := os.ReadFile(external)
				if kind == "dangling-archive" {
					if !os.IsNotExist(err) {
						t.Errorf("ZIP download created an external target: %q, %v", data, err)
					}
				} else if err != nil || string(data) != "external content" {
					t.Errorf("ZIP download changed external content: %q, %v", data, err)
				}
				info, err := os.Lstat(link)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "archive-directory" {
					if !info.IsDir() {
						t.Error("ZIP download replaced the directory")
					}
				} else if info.Mode()&os.ModeSymlink == 0 {
					t.Error("ZIP download replaced the symlink")
				}
			})
		}
	}
}

func TestForcedRegistrationRejectsSymlinkedOutputsBeforeIssuingCredentials(t *testing.T) {
	cases := []struct {
		name, link, external, targetDir string
		directory, zip                  bool
	}{
		{"bundle", "", "generated/security/client.key", "", true, false},
		{"generated", "generated", "security/client.key", "", true, false},
		{"security", "generated/security", "client.key", "", true, false},
		{"private-key", "generated/security/client.key", "client.key", "", false, false},
		{"application", "app.xml", "app.xml", "", false, false},
		{"archive", "", "bundle.zip", "", false, true},
		{"custom bundle", "", "generated/security/client.key", "nested/custom", true, false},
		{"custom security", "generated/security", "client.key", "absolute", true, false},
		{"custom archive folder", "", "db-app-client.zip", "nested/custom", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			outside := t.TempDir()
			external := filepath.Join(outside, tc.external)
			if err := os.MkdirAll(filepath.Dir(external), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(external, []byte("external content"), 0o600); err != nil {
				t.Fatal(err)
			}
			targetDir := tc.targetDir
			if targetDir == "absolute" {
				targetDir = filepath.Join(t.TempDir(), "custom")
			}
			destination := "db-app-client"
			if targetDir != "" {
				destination = targetDir
			}
			if tc.zip && targetDir == "" {
				destination += ".zip"
			}
			link := filepath.Join(destination, tc.link)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			target := external
			if tc.directory {
				target = outside
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlink creation unavailable: %v", err)
			}
			api := splitAPI()
			runner := New(api, io.Discard)
			generatedKey := false
			runner.CSRGenerator = func(string, string, string) ([]byte, string, error) {
				generatedKey = true
				return []byte("new private key"), "CSR", nil
			}

			if err := runner.RegisterAppClientWithOptions("db", "app", "client", ApplicationRegistrationOptions{TargetDir: targetDir, GeneratePrivateKey: true, ForceOverwrite: true, ZIP: tc.zip}); err == nil {
				t.Error("registration accepted a symlinked output")
			}
			if generatedKey || api.lastPayload != nil {
				t.Error("unsafe output caused key generation or client registration")
			}
			if data, err := os.ReadFile(external); err != nil || string(data) != "external content" {
				t.Errorf("registration changed the external file: %q, %v", data, err)
			}
		})
	}
}

func TestForcedRegistrationRejectsWrongOutputTypesBeforeIssuingCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, path, kind string
		zip              bool
	}{
		{"application directory", "app.xml", "directory", false},
		{"manifest directory", "manifest.json", "directory", false},
		{"generated artifact directory", "generated/app.qos.xml", "directory", false},
		{"private key directory", "generated/security/client.key", "directory", false},
		{"certificate directory", "generated/security/identity.pem", "directory", false},
		{"security file", "generated/security", "file", false},
		{"application socket", "app.xml", "socket", false},
		{"archive socket", "", "socket", true},
		{"bundle socket", "", "socket", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			destination := "db-app-client"
			if tc.zip {
				destination += ".zip"
			}
			path := filepath.Join(destination, tc.path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			switch tc.kind {
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(path, []byte("existing content"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Skipf("Unix sockets unavailable: %v", err)
				}
				defer listener.Close()
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			api := splitAPI()
			runner := New(api, io.Discard)
			generatedKey := false
			runner.CSRGenerator = func(string, string, string) ([]byte, string, error) {
				generatedKey = true
				return []byte("private key"), "csr", nil
			}
			err = runner.RegisterAppClientWithOptions("db", "app", "client", ApplicationRegistrationOptions{GeneratePrivateKey: true, ForceOverwrite: true, ZIP: tc.zip})
			if err == nil {
				t.Error("registration accepted an invalid destination type")
			}
			if generatedKey || api.lastPayload != nil {
				t.Error("invalid destination caused key generation or client registration")
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Errorf("invalid output was modified: %v", err)
			}
		})
	}
}

func TestSplitRefreshRejectsSpecialFileBeforeChangingAnyArtifacts(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("generated", 0o700); err != nil {
		t.Fatal(err)
	}
	qos := filepath.Join("generated", "app.qos.xml")
	if err := os.WriteFile(qos, []byte("existing qos"), 0o600); err != nil {
		t.Fatal(err)
	}
	types := filepath.Join("generated", "app.types.xml")
	listener, err := net.Listen("unix", types)
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	before, err := os.Lstat(types)
	if err != nil {
		t.Fatal(err)
	}

	if err := New(splitAPI(), io.Discard).DownloadApplication("db", "app", ApplicationDownloadOptions{}); err == nil {
		t.Error("refresh accepted a socket as a generated artifact")
	}
	if data, err := os.ReadFile(qos); err != nil || string(data) != "existing qos" {
		t.Errorf("refresh changed QoS before rejecting the types destination: %q, %v", data, err)
	}
	if after, err := os.Lstat(types); err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Errorf("refresh modified the types destination: %v", err)
	}
}
