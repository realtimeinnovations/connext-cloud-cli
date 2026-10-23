// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package commands

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
			if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
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

// Each route has its own dispatch/overwrite behavior, but all must reach the
// common writer. The filesystem edge-case matrix lives in internal/fileoutput.
func TestApplicationDownloadRoutesAcceptSelectedBaseSymlinks(t *testing.T) {
	for _, route := range []string{"generated", "example", "zip", "config", "gateway", "collector"} {
		t.Run(route, func(t *testing.T) {
			outside := t.TempDir()
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			target := filepath.Join(link, "new", "bundle")
			options := ApplicationDownloadOptions{TargetDir: target, ForceOverwrite: true}
			api := splitAPI()
			switch route {
			case "example":
				options.GenerateExample = true
			case "zip":
				options.ZIP = true
			case "config":
				options.ConfigOutput = filepath.Join(target, "config.json")
			case "gateway", "collector":
				kind := "gateway"
				if route == "collector" {
					kind = "telemetry-service-collector"
				}
				api = registrationTestAPI(map[string]any{"client_data": map[string]any{"kind": kind}, "client_config": "<dds/>"})
			}
			if route == "config" {
				api = registrationTestAPI(map[string]any{"client_data": map[string]any{"kind": "app"}})
			}
			if err := New(api, io.Discard).DownloadApplication("db", "app", options); err != nil {
				t.Fatal(err)
			}
			name := "generated/app.qos.xml"
			switch route {
			case "zip":
				name = "db-app.zip"
			case "config":
				name = "config.json"
			case "gateway", "collector":
				name = "app.xml"
			case "example":
				name = "app.py"
			}
			data, err := os.ReadFile(filepath.Join(outside, "new", "bundle", name))
			if err != nil || len(data) == 0 {
				t.Fatalf("expected artifact %s: %q, %v", name, data, err)
			}
		})
	}
}

func TestRegistrationPreflightsBeforeCredentialIssuance(t *testing.T) {
	for _, location := range []string{"generated-symlink", "zip-symlink", "artifact", "security-entry"} {
		t.Run(location, func(t *testing.T) {
			root := t.TempDir()
			destination := filepath.Join(root, "bundle")
			zipOutput := location == "zip-symlink"
			if location == "generated-symlink" || zipOutput {
				if err := os.MkdirAll(destination, 0700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(destination, "generated")
				target := t.TempDir()
				if zipOutput {
					link = filepath.Join(destination, "db-app-client.zip")
					target = filepath.Join(target, "output.zip")
					if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}

			} else {
				name := "app.xml"
				if location == "security-entry" {
					name = "generated/security/server-supplied.pem"
				}
				if err := os.MkdirAll(filepath.Join(destination, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			api := splitAPI()
			runner := New(api, io.Discard)
			runner.CSRGenerator = func(string, string, string) ([]byte, string, error) {
				t.Fatal("invalid output generated credentials")
				return nil, "", nil
			}
			if err := runner.RegisterAppClientWithOptions("db", "app", "client", ApplicationRegistrationOptions{TargetDir: destination, GeneratePrivateKey: true, ForceOverwrite: true, ZIP: zipOutput}); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("want unsafe output error, got %v", err)
			}
			if api.lastPayload != nil {
				t.Fatal("invalid destination issued credentials")
			}
			if _, err := os.Stat(filepath.Join(root, "bundle", "manifest.json")); !os.IsNotExist(err) {
				t.Fatalf("preflight created output: %v", err)
			}
		})
	}
}

func TestRegistrationWritesBundleThroughSelectedSymlink(t *testing.T) {
	for _, zipOutput := range []bool{false, true} {
		t.Run(fmt.Sprint(zipOutput), func(t *testing.T) {
			base := t.TempDir()
			link := filepath.Join(t.TempDir(), "selected")
			if err := os.Symlink(base, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			runner := New(splitAPI(), io.Discard)
			runner.CSRGenerator = func(string, string, string) ([]byte, string, error) { return []byte("private key"), "csr", nil }
			if err := runner.RegisterAppClientWithOptions("db", "app", "client", ApplicationRegistrationOptions{TargetDir: link, ForceOverwrite: true, ZIP: zipOutput, GeneratePrivateKey: true}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(base, "generated", "security", "identity.pem")
			if zipOutput {
				path = filepath.Join(base, "db-app-client.zip")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !zipOutput {
				if string(data) != "certificate" {
					t.Fatalf("credential=%q", data)
				}
			} else {
				archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, entry := range archive.File {
					if strings.HasSuffix(entry.Name, "/generated/security/identity.pem") {
						file, err := entry.Open()
						if err != nil {
							t.Fatal(err)
						}
						content, err := io.ReadAll(file)
						file.Close()
						if err != nil || string(content) != "certificate" {
							t.Fatalf("credential=%q, %v", content, err)
						}
						found = true
					}
				}
				if !found {
					t.Fatal("archive omitted issued credential")
				}
			}
		})
	}
}
