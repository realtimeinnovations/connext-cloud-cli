// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package commands

import (
	"archive/zip"
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	mgcrypto "github.com/realtimeinnovations/connext-cloud-cli/crypto"
)

func TestCreateApplicationMapsObservabilityCollectorKind(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /databuses/obs/applications": newTextResponse(http.StatusCreated, "ok")}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.CreateApplication("obs", "collector", 7777, "observability-collector", "", false); err != nil {
		t.Fatal(err)
	}
	payload, ok := api.lastPayload.(map[string]any)
	if !ok || payload["kind"] != "telemetry-service-collector" {
		t.Fatalf("unexpected payload: %#v", api.lastPayload)
	}
}

func TestCreateApplicationPrintsJSONResponse(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /databuses/db/applications": newJSONResponse(http.StatusCreated, map[string]any{
		"client_config": nil,
		"message":       "gateway config generated and saved",
	})}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.CreateApplication("db", "gateway", 7777, "gateway", "", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\n  \"message\": \"gateway config generated and saved\"\n") {
		t.Fatalf("expected formatted JSON output, got: %s", out.String())
	}
}

func TestCreateApplicationReadsManifest(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /databuses/db/applications": newTextResponse(http.StatusCreated, "ok")}}
	var out bytes.Buffer
	runner := New(api, &out)
	manifestPath := applicationInputFile(t, "application.json", `{"port": 9000, "kind": "gateway", "topic_data": {"0": {"domainId": 0}}}`)
	if err := runner.CreateApplication("db", "gateway", 8000, "", manifestPath, true); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["port"] != 8000 || payload["kind"] != "gateway" || payload["client_name"] != "gateway" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	topicData, ok := payload["topic_data"].(map[string]any)
	if !ok || topicData["0"] == nil {
		t.Fatalf("unexpected topic data: %#v", payload)
	}
}

func TestCreateApplicationAcceptsStringPort(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /databuses/db/applications": newTextResponse(http.StatusCreated, "ok")}}
	runner := New(api, io.Discard)
	manifestPath := applicationInputFile(t, "application.json", `{"port": "9000"}`)
	if err := runner.CreateApplication("db", "application", 7777, "", manifestPath, false); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["port"] != 9000 {
		t.Fatalf("unexpected port: %#v", payload)
	}
}

func TestCreateApplicationDefaultsOptionalManifestFields(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /databuses/db/applications": newTextResponse(http.StatusCreated, "ok")}}
	var out bytes.Buffer
	runner := New(api, &out)
	manifestPath := applicationInputFile(t, "application.json", `{}`)
	if err := runner.CreateApplication("db", "application", 7777, "", manifestPath, false); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["port"] != 7777 || payload["kind"] != "app" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if topicData, ok := payload["topic_data"].(map[string]any); !ok || len(topicData) != 0 {
		t.Fatalf("unexpected topic data: %#v", payload)
	}
}

func TestCreateApplicationRejectsNonObjectTopicData(t *testing.T) {
	runner := New(&fakeAPI{}, io.Discard)
	manifestPath := applicationInputFile(t, "application.json", `{"topic_data": []}`)
	err := runner.CreateApplication("db", "application", 7777, "", manifestPath, false)
	if err == nil || !strings.Contains(err.Error(), "topic_data must be a JSON object") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetApplicationWritesManifestWithoutXML(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/subscriber": newJSONResponse(http.StatusOK, map[string]any{
		"client_config": "<participant/>",
		"client_data":   map[string]any{"port": "7777", "kind": "app", "topics": map[string]any{"0": map[string]any{"domainId": 0}}},
	})}}
	var out bytes.Buffer
	runner := New(api, &out)
	target := filepath.Join(t.TempDir(), "configs", "application.json")
	if err := runner.GetApplication("db", "subscriber", false, false, "", target); err != nil {
		t.Fatal(err)
	}
	savedData := readApplicationFile(t, target)
	if bytes.Contains(savedData, []byte("client_config")) {
		t.Fatalf("unexpected saved manifest: %s", savedData)
	}
	var manifest map[string]any
	if err := json.Unmarshal(savedData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["kind"] != "app" || manifest["port"].(float64) != 7777 || manifest["topic_data"] == nil {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func TestSaveClientFileCreatesPrivateParentDirectoryForKey(t *testing.T) {
	runner := New(&fakeAPI{}, io.Discard)
	target := filepath.Join(t.TempDir(), "keys", "client.key")
	if _, err := runner.SaveClientFile("", target, []byte("key"), false); err != nil {
		t.Fatal(err)
	}
	if data := readApplicationFile(t, target); string(data) != "key" {
		t.Fatalf("key = %q", data)
	}
	if runtime.GOOS == "windows" {
		return // Windows does not expose Unix file permission bits.
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(target): 0700, target: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode: %v, error=%v; want %o", path, info, err, mode)
		}
	}
}

func TestGetApplicationDefaultsMissingTopicsInManifest(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/subscriber": newJSONResponse(http.StatusOK, map[string]any{
		"client_data": map[string]any{"kind": "app"},
	})}}
	runner := New(api, io.Discard)
	target := filepath.Join(t.TempDir(), "application.json")
	if err := runner.GetApplication("db", "subscriber", false, false, "", target); err != nil {
		t.Fatal(err)
	}
	savedData := readApplicationFile(t, target)
	var manifest map[string]any
	if err := json.Unmarshal(savedData, &manifest); err != nil {
		t.Fatal(err)
	}
	if topicData, ok := manifest["topic_data"].(map[string]any); !ok || len(topicData) != 0 {
		t.Fatalf("unexpected topic data: %#v", manifest)
	}
}

func TestGetGatewayDownloadsXMLWithoutManifestOutput(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/subscriber": newJSONResponse(http.StatusOK, map[string]any{
		"client_config": "<participant/>",
		"client_data":   map[string]any{"kind": "gateway"},
	})}}
	var out bytes.Buffer
	runner := New(api, &out)
	dir := t.TempDir()
	if err := runner.GetApplication("db", "subscriber", false, false, dir, ""); err != nil {
		t.Fatal(err)
	}
	if data := readApplicationFile(t, filepath.Join(dir, "subscriber.xml")); string(data) != "<participant/>" {
		t.Fatalf("XML = %q", data)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("unexpected output files: %v, error=%v", entries, err)
	}
	if strings.Contains(out.String(), "map[") {
		t.Fatalf("unexpected application data output: %s", out.String())
	}
}

func TestDownloadApplicationWritesManagerArtifactsAndWarnsForUnknownTypes(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/shapes": newJSONResponse(http.StatusOK, map[string]any{
		"bundle": testApplicationBundle("<dds/>", "# example"),
		"manifest": map[string]any{
			"version": 1,
			"topics": []map[string]any{
				{"name": "Square", "type": "ShapeType"},
				{"name": "FutureTopic", "type": "unknown"},
			},
		},
	})}}
	var out bytes.Buffer
	runner := New(api, &out)
	dir := t.TempDir()
	if err := runner.DownloadApplication("db", "shapes", ApplicationDownloadOptions{TargetDir: dir, IncludeManifest: true}); err != nil {
		t.Fatal(err)
	}
	if data := readApplicationFile(t, filepath.Join(dir, "generated", "shapes.qos.xml")); string(data) != "<dds/>" {
		t.Fatalf("XML = %q", data)
	}
	if data := readApplicationFile(t, filepath.Join(dir, "manifest.json")); !bytes.Contains(data, []byte(`"FutureTopic"`)) {
		t.Fatalf("manifest = %s", data)
	}
	if !strings.Contains(out.String(), "Warning: Type information is unavailable for: FutureTopic") {
		t.Fatalf("missing unknown-type warning: %s", out.String())
	}
}

func TestDownloadApplicationBundleMatchesWebLayout(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/demo/applications/shapes": newJSONResponse(http.StatusOK, map[string]any{
		"bundle":   testApplicationBundle("<dds/>", "print('shapes')\n"),
		"manifest": map[string]any{"version": 1, "application": "shapes", "topics": []any{}},
	})}}
	runner := New(api, io.Discard)
	dir := t.TempDir()
	if err := runner.DownloadApplication("demo", "shapes", ApplicationDownloadOptions{TargetDir: dir, ZIP: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "demo-shapes.zip")
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	files := map[string]*zip.File{}
	for _, file := range archive.File {
		files[file.Name] = file
	}
	for _, name := range []string{"demo-shapes/shapes.xml", "demo-shapes/shapes.py", "demo-shapes/manifest.json"} {
		if files[name] == nil {
			t.Fatalf("bundle missing %s: %#v", name, files)
		}
	}
}

func TestDownloadApplicationBundleHonorsExistingFile(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/demo/applications/shapes": newJSONResponse(http.StatusOK, map[string]any{
		"bundle":   testApplicationBundle("<dds/>", "# example\n"),
		"manifest": map[string]any{"version": 1, "topics": []any{}},
	})}}
	var out bytes.Buffer
	runner := New(api, &out)
	dir := t.TempDir()
	path := filepath.Join(dir, "demo-shapes.zip")
	if err := os.WriteFile(path, []byte("existing archive"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := runner.DownloadApplication("demo", "shapes", ApplicationDownloadOptions{TargetDir: dir, ZIP: true}); err == nil {
		t.Fatal("expected existing archive error")
	}
	if data := readApplicationFile(t, path); string(data) != "existing archive" {
		t.Fatalf("existing archive changed: %q", data)
	}
	if !strings.Contains(out.String(), "demo-shapes.zip already exists. Use -f to overwrite.") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestApplicationBundlePlacesSecureFilesAndProtectsPrivateKey(t *testing.T) {
	bundle, err := applicationBundle("demo", "shapes", "client-1", applicationArtifacts{
		Bundle:   testApplicationBundle(`<dds path="./generated/security/identity.pem"/>`, "# example\n"),
		Manifest: json.RawMessage(`{"version":1,"topics":[]}`),
	}, map[string]string{"identity.pem": base64.StdEncoding.EncodeToString([]byte("certificate")), "CLIENT.KEY": "ignored-invalid-server-copy"}, []byte("private key"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*zip.File{}
	for _, file := range archive.File {
		files[file.Name] = file
	}
	root := "demo-shapes-client-1/"
	if files[root+"generated/security/CLIENT.KEY"] != nil {
		t.Fatal("archive contains the conflicting server key")
	}
	if files[root+"shapes.xml"] == nil || files[root+"manifest.json"] == nil || files[root+"generated/security/identity.pem"] == nil {
		t.Fatalf("unexpected secure bundle: %#v", files)
	}
	key := files[root+"generated/security/client.key"]
	if key == nil || key.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %v, want 0600", key)
	}
	reader, err := key.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil || string(data) != "private key" {
		t.Fatalf("archived key=%q, %v", data, err)
	}
}

func TestRegisterAppClientWritesCompleteDirectoryOrSecureZIP(t *testing.T) {
	for _, test := range []struct {
		name, targetDir string
		zip             bool
	}{
		{"directory", "", false},
		{"zip", "", true},
		{"relative directory", "nested/custom bundle", false},
		{"relative zip", "nested/custom bundle", true},
		{"absolute directory", "absolute", false},
		{"absolute zip", "absolute", true},
		{"symlink directory", "symlink", false},
		{"symlink zip", "symlink", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			targetDir := test.targetDir
			if targetDir == "absolute" {
				targetDir = filepath.Join(t.TempDir(), "custom bundle")
			}
			if targetDir == "symlink" {
				targetDir = filepath.Join(t.TempDir(), "selected")
				if err := os.Symlink(t.TempDir(), targetDir); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			api := &fakeAPI{responses: map[string]*http.Response{
				"POST /databuses/demo/applications/shapes/clients": newJSONResponse(http.StatusCreated, map[string]any{
					"client_id": "client-1",
					"secure_files": map[string]any{
						"identity.pem": base64.StdEncoding.EncodeToString([]byte("certificate")),
					},
				}),
				"GET /databuses/demo/applications/shapes": newJSONResponse(http.StatusOK, map[string]any{
					"bundle":   testApplicationBundle(`<dds path="./generated/security/identity.pem"/>`, "# example\n"),
					"manifest": map[string]any{"version": 1, "application": "shapes", "topics": []any{}},
				}),
			}}
			runner := New(api, io.Discard)
			runner.CSRGenerator = mgcrypto.GeneratePrivateKeyAndCSR
			if err := runner.RegisterAppClientWithOptions("demo", "shapes", "client-1", ApplicationRegistrationOptions{TargetDir: targetDir, GeneratePrivateKey: true, ZIP: test.zip, ForceOverwrite: test.targetDir == "symlink"}); err != nil {
				t.Fatal(err)
			}
			root := "demo-shapes-client-1"
			archivePath := root + ".zip"
			if targetDir != "" {
				if test.zip {
					archivePath = filepath.Join(targetDir, archivePath)
				} else {
					root = targetDir
				}
				if _, err := os.Stat("demo-shapes-client-1"); !os.IsNotExist(err) {
					t.Fatalf("custom output created the default directory: %v", err)
				}
			}
			expected := map[string]string{
				"shapes.xml":                      `<dds path="./generated/security/identity.pem"/>`,
				"shapes.py":                       "# example\n",
				"generated/shapes.qos.xml":        "<dds/>",
				"generated/shapes.types.xml":      "<types/>",
				"README.md":                       "# Instructions",
				"generated/security/identity.pem": "certificate",
			}
			if test.zip {
				info, err := os.Stat(archivePath)
				if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
					t.Fatalf("private ZIP info = %v, error = %v; want mode 0600", info, err)
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("ZIP output must not create a directory; stat error: %v", err)
				}
				archive, err := zip.OpenReader(archivePath)
				if err != nil {
					t.Fatal(err)
				}
				defer archive.Close()
				files := map[string]*zip.File{}
				for _, file := range archive.File {
					files[file.Name] = file
				}
				for name, want := range expected {
					file := files[root+"/"+name]
					if file == nil {
						t.Fatalf("ZIP missing %s", name)
					}
					reader, err := file.Open()
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(reader)
					reader.Close()
					if err != nil || string(data) != want {
						t.Fatalf("ZIP entry %s = %q, error = %v; want %q", name, data, err, want)
					}
				}
				if files[root+"/manifest.json"] == nil {
					t.Fatalf("unexpected ZIP contents: %#v", files)
				}
				keyFile := files[root+"/generated/security/client.key"]
				if keyFile == nil {
					t.Fatal("archive missing generated private key")
				}
				keyReader, err := keyFile.Open()
				if err != nil {
					t.Fatal(err)
				}
				keyData, err := io.ReadAll(keyReader)
				keyReader.Close()
				if err != nil {
					t.Fatal(err)
				}
				assertApplicationKeyMatchesCSR(t, keyData, api.lastPayload.(map[string]any)["csr"].(string), "demo.shapes.client-1")
				if mode := keyFile.Mode().Perm(); mode != 0o600 {
					t.Fatalf("private key mode = %#o, want 0600", mode)
				}
				return
			}
			if _, err := os.Stat(archivePath); !os.IsNotExist(err) {
				t.Fatalf("directory output must not create a ZIP; stat error: %v", err)
			}
			for name, want := range expected {
				data, err := os.ReadFile(filepath.Join(root, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s = %q, error = %v; want %q", name, data, err, want)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "manifest.json")); err != nil {
				t.Fatal(err)
			}
			assertApplicationKeyMatchesCSR(t, readApplicationFile(t, filepath.Join(root, "generated/security/client.key")), api.lastPayload.(map[string]any)["csr"].(string), "demo.shapes.client-1")
			keyInfo, err := os.Stat(filepath.Join(root, "generated/security/client.key"))
			if err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && keyInfo.Mode().Perm() != 0o600 {
				t.Fatalf("private key mode = %#o, want 0600", keyInfo.Mode().Perm())
			}
		})
	}
}

func TestSecureFilesRejectUnsafeNamesBeforeWriting(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../../outside.pem", "nested/identity.pem", "/outside.pem", `..\outside.pem`, `nested\identity.pem`, `C:outside.pem`, "bad\x00name"} {
		t.Run(name, func(t *testing.T) {
			files := map[string]string{
				"identity.pem": base64.StdEncoding.EncodeToString([]byte("certificate")),
				name:           base64.StdEncoding.EncodeToString([]byte("outside")),
			}
			runner := New(&fakeAPI{}, io.Discard)
			dir := t.TempDir()
			if err := runner.SaveSecureFiles(files, nil, false, dir); err == nil || !strings.Contains(err.Error(), "invalid secure filename") {
				t.Fatalf("unexpected directory error: %v", err)
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatalf("invalid response wrote files: %v, %v", entries, err)
			}
			for _, config := range []string{"<dds/>", `<dds path="./generated/security/identity.pem"/>`} {
				bundle, err := applicationBundle("db", "app", "client", applicationArtifacts{
					Bundle: testApplicationBundle(config, "# example"), Manifest: json.RawMessage(`{"topics":[]}`),
				}, files, nil)
				if err == nil || !strings.Contains(err.Error(), "invalid secure filename") || len(bundle) != 0 {
					t.Fatalf("unexpected bundle result: %d bytes, error = %v", len(bundle), err)
				}
			}
		})
	}
}

func registrationTestAPI(artifacts map[string]any) *fakeAPI {
	return &fakeAPI{responses: map[string]*http.Response{
		"POST /databuses/db/applications/app/clients": newJSONResponse(http.StatusCreated, map[string]any{
			"client_id":    "client",
			"secure_files": map[string]string{"identity.pem": base64.StdEncoding.EncodeToString([]byte("certificate"))},
		}),
		"GET /databuses/db/applications/app": newJSONResponse(http.StatusOK, artifacts),
	}}
}

func TestRegisterAppClientRejectsIncompleteArtifactsInBothModes(t *testing.T) {
	for _, field := range []string{"bundle", "manifest"} {
		for _, value := range []any{nil, ""} {
			for _, zipOutput := range []bool{false, true} {
				label := field + "/missing/directory"
				if value != nil {
					label = field + "/empty/directory"
				}
				if zipOutput {
					label = strings.TrimSuffix(label, "directory") + "zip"
				}
				t.Run(label, func(t *testing.T) {
					dir := t.TempDir()
					t.Chdir(dir)
					artifacts := map[string]any{"bundle": testApplicationBundle("<dds/>", "# example"), "manifest": map[string]any{"topics": []any{}}}
					if value == nil {
						delete(artifacts, field)
					} else if field == "manifest" {
						artifacts[field] = nil
					} else {
						artifacts[field] = value
					}
					api := registrationTestAPI(artifacts)
					runner := New(api, io.Discard)
					runner.CSRGenerator = func(string, string, string) ([]byte, string, error) { return []byte("key"), "csr", nil }
					err := runner.RegisterAppClientWithOptions("db", "app", "client", ApplicationRegistrationOptions{GeneratePrivateKey: true, ZIP: zipOutput})
					if err == nil {
						t.Fatalf("unexpected error: %v", err)
					}
					if api.lastPayload != nil {
						t.Fatalf("incomplete artifacts caused client registration: %#v", api.lastPayload)
					}
					entries, err := os.ReadDir(dir)
					if err != nil || len(entries) != 0 {
						t.Fatalf("incomplete artifacts created output: %v; error = %v", entries, err)
					}
				})
			}
		}
	}
}

// The complete bundle test covers new archives; this case checks replacement
// of an existing public archive when the replacement contains a private key.
func TestRegistrationZIPOverwriteProtectsGeneratedKey(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "db-app-client.zip")
	if err := os.WriteFile(output, []byte("old archive"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(output, 0644); err != nil {
		t.Fatal(err)
	}
	api := registrationTestAPI(map[string]any{
		"bundle": testApplicationBundle("<dds/>", "# example"), "manifest": map[string]any{"topics": []any{}},
	})
	runner := New(api, io.Discard)
	runner.CSRGenerator = mgcrypto.GeneratePrivateKeyAndCSR
	if err := runner.RegisterAppClientWithOptions("db", "app", "client", ApplicationRegistrationOptions{TargetDir: dir, GeneratePrivateKey: true, ForceOverwrite: true, ZIP: true}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("private archive info=%v, %v", info, err)
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	key, err := archive.Open("db-app-client/generated/security/client.key")
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	data, err := io.ReadAll(key)
	if err != nil {
		t.Fatal(err)
	}
	assertApplicationKeyMatchesCSR(t, data, api.lastPayload.(map[string]any)["csr"].(string), "db.app.client")
}

func applicationInputFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readApplicationFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertApplicationKeyMatchesCSR(t *testing.T, keyPEM []byte, csrPEM, commonName string) {
	t.Helper()
	keyBlock, _ := pem.Decode(keyPEM)
	csrBlock, _ := pem.Decode([]byte(csrPEM))
	if keyBlock == nil || csrBlock == nil {
		t.Fatal("missing PEM key or CSR")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatal(err)
	}
	if !key.PublicKey.Equal(csr.PublicKey) || csr.Subject.CommonName != commonName {
		t.Fatalf("persisted key does not match CSR for %q: subject=%v", commonName, csr.Subject)
	}
}
