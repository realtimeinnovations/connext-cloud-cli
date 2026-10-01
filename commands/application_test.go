// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package commands

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	runner.ReadFile = func(fileName string) ([]byte, error) {
		if fileName != "application.json" {
			t.Fatalf("unexpected configuration file: %s", fileName)
		}
		return []byte(`{"port": 9000, "kind": "gateway", "topic_data": {"0": {"domainId": 0}}}`), nil
	}
	if err := runner.CreateApplication("db", "gateway", 8000, "", "application.json", true); err != nil {
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
	runner.ReadFile = func(string) ([]byte, error) { return []byte(`{"port": "9000"}`), nil }
	if err := runner.CreateApplication("db", "application", 7777, "", "application.json", false); err != nil {
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
	runner.ReadFile = func(string) ([]byte, error) { return []byte(`{}`), nil }
	if err := runner.CreateApplication("db", "application", 7777, "", "application.json", false); err != nil {
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
	runner.ReadFile = func(string) ([]byte, error) { return []byte(`{"topic_data": []}`), nil }
	err := runner.CreateApplication("db", "application", 7777, "", "application.json", false)
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
	var savedName string
	var savedData []byte
	runner.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	runner.WriteFile = func(fileName string, data []byte, _ os.FileMode) error {
		savedName, savedData = fileName, data
		return nil
	}
	if err := runner.GetApplication("db", "subscriber", false, false, "", "application.json"); err != nil {
		t.Fatal(err)
	}
	if savedName != "application.json" || bytes.Contains(savedData, []byte("client_config")) {
		t.Fatalf("unexpected saved manifest %q: %s", savedName, savedData)
	}
	var manifest map[string]any
	if err := json.Unmarshal(savedData, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["kind"] != "app" || manifest["port"].(float64) != 7777 || manifest["topic_data"] == nil {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func TestGetApplicationCreatesManifestParentDirectory(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/subscriber": newJSONResponse(http.StatusOK, map[string]any{
		"client_data": map[string]any{"kind": "app"},
	})}}
	runner := New(api, io.Discard)
	runner.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	var createdDir string
	runner.MkdirAll = func(dir string, _ os.FileMode) error {
		createdDir = dir
		return nil
	}
	runner.WriteFile = func(string, []byte, os.FileMode) error { return nil }
	if err := runner.GetApplication("db", "subscriber", false, false, "", "configs/application.json"); err != nil {
		t.Fatal(err)
	}
	if createdDir != "configs" {
		t.Fatalf("created directory = %q, want configs", createdDir)
	}
}

func TestSaveClientFileCreatesPrivateParentDirectoryForKey(t *testing.T) {
	runner := New(&fakeAPI{}, io.Discard)
	runner.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	var createdDir string
	var createdMode os.FileMode
	var fileMode os.FileMode
	var chmodMode os.FileMode
	runner.MkdirAll = func(dir string, mode os.FileMode) error {
		createdDir, createdMode = dir, mode
		return nil
	}
	runner.WriteFile = func(_ string, _ []byte, mode os.FileMode) error {
		fileMode = mode
		return nil
	}
	runner.Chmod = func(_ string, mode os.FileMode) error {
		chmodMode = mode
		return nil
	}
	if _, err := runner.SaveClientFile("", "keys/client.key", []byte("key"), false); err != nil {
		t.Fatal(err)
	}
	if createdDir != "keys" || createdMode != 0o700 {
		t.Fatalf("created directory = %q with mode %#o, want keys with mode 0700", createdDir, createdMode)
	}
	if fileMode != 0o600 || chmodMode != 0o600 {
		t.Fatalf("write mode = %#o, chmod mode = %#o, want 0600", fileMode, chmodMode)
	}
}

func TestGetApplicationDefaultsMissingTopicsInManifest(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/subscriber": newJSONResponse(http.StatusOK, map[string]any{
		"client_data": map[string]any{"kind": "app"},
	})}}
	runner := New(api, io.Discard)
	runner.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	var savedData []byte
	runner.WriteFile = func(_ string, data []byte, _ os.FileMode) error {
		savedData = data
		return nil
	}
	if err := runner.GetApplication("db", "subscriber", false, false, "", "application.json"); err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(savedData, &manifest); err != nil {
		t.Fatal(err)
	}
	if topicData, ok := manifest["topic_data"].(map[string]any); !ok || len(topicData) != 0 {
		t.Fatalf("unexpected topic data: %#v", manifest)
	}
}

func TestGetApplicationDownloadsXMLWithoutManifestOutput(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/subscriber": newJSONResponse(http.StatusOK, map[string]any{
		"client_config": "<participant/>",
		"client_data":   map[string]any{"kind": "app"},
	})}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	var savedName string
	runner.WriteFile = func(fileName string, _ []byte, _ os.FileMode) error {
		savedName = fileName
		return nil
	}
	if err := runner.GetApplication("db", "subscriber", false, false, "", ""); err != nil {
		t.Fatal(err)
	}
	if savedName != "subscriber.xml" {
		t.Fatalf("saved file = %q, want subscriber.xml", savedName)
	}
	if strings.Contains(out.String(), "map[") {
		t.Fatalf("unexpected application data output: %s", out.String())
	}
}

func TestDownloadApplicationWritesManagerArtifactsAndWarnsForUnknownTypes(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/db/applications/shapes": newJSONResponse(http.StatusOK, map[string]any{
		"client_config": "<dds/>",
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
	runner.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	written := map[string][]byte{}
	runner.WriteFile = func(name string, data []byte, _ os.FileMode) error {
		written[name] = append([]byte(nil), data...)
		return nil
	}
	if err := runner.DownloadApplication("db", "shapes", ApplicationDownloadOptions{IncludeManifest: true}); err != nil {
		t.Fatal(err)
	}
	if string(written["shapes.xml"]) != "<dds/>" {
		t.Fatalf("unexpected XML artifacts: %#v", written)
	}
	if !bytes.Contains(written["manifest.json"], []byte(`"FutureTopic"`)) {
		t.Fatalf("unexpected manifest: %s", written["manifest.json"])
	}
	if !strings.Contains(out.String(), "Warning: Type information is unavailable for: FutureTopic") {
		t.Fatalf("missing unknown-type warning: %s", out.String())
	}
}

func TestDownloadApplicationBundleMatchesWebLayout(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/demo/applications/shapes": newJSONResponse(http.StatusOK, map[string]any{
		"client_config":  "<dds/>",
		"client_example": "print('shapes')\n",
		"manifest":       map[string]any{"version": 1, "application": "shapes", "topics": []any{}},
	})}}
	runner := New(api, io.Discard)
	runner.Stat = func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	var bundle []byte
	runner.WriteFile = func(name string, data []byte, mode os.FileMode) error {
		if name != "demo-shapes.zip" || mode != 0o644 {
			t.Fatalf("wrote %q with mode %#o", name, mode)
		}
		bundle = append([]byte(nil), data...)
		return nil
	}
	if err := runner.DownloadApplication("demo", "shapes", ApplicationDownloadOptions{ZIP: true}); err != nil {
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
	for _, name := range []string{"demo-shapes/shapes.xml", "demo-shapes/shapes.py", "demo-shapes/manifest.json"} {
		if files[name] == nil {
			t.Fatalf("bundle missing %s: %#v", name, files)
		}
	}
}

func TestDownloadApplicationBundleHonorsExistingFile(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses/demo/applications/shapes": newJSONResponse(http.StatusOK, map[string]any{
		"client_config":  "<dds/>",
		"client_example": "# example\n",
		"manifest":       map[string]any{"version": 1, "topics": []any{}},
	})}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.Stat = func(string) (os.FileInfo, error) { return nil, nil }
	runner.WriteFile = func(string, []byte, os.FileMode) error {
		t.Fatal("existing bundle must not be overwritten")
		return nil
	}
	if err := runner.DownloadApplication("demo", "shapes", ApplicationDownloadOptions{ZIP: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "demo-shapes.zip already exists. Use -f to overwrite.") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestApplicationBundlePlacesSecureFilesAndProtectsPrivateKey(t *testing.T) {
	bundle, err := applicationBundle("demo", "shapes", "client-1", applicationArtifacts{
		ClientConfig:  `<dds path="./secure/identity.pem"/>`,
		ClientExample: "# example\n",
		Manifest:      json.RawMessage(`{"version":1,"topics":[]}`),
	}, map[string]string{"identity.pem": base64.StdEncoding.EncodeToString([]byte("certificate"))}, []byte("private key"))
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
	if files[root+"shapes.xml"] == nil || files[root+"manifest.json"] == nil || files[root+"secure/identity.pem"] == nil {
		t.Fatalf("unexpected secure bundle: %#v", files)
	}
	key := files[root+"secure/client.key"]
	if key == nil || key.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %v, want 0600", key)
	}
}

func TestRegisterAppClientWritesCompleteDirectoryOrSecureZIP(t *testing.T) {
	for _, test := range []struct {
		name string
		zip  bool
	}{
		{"directory", false},
		{"zip", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			api := &fakeAPI{responses: map[string]*http.Response{
				"POST /databuses/demo/applications/shapes/clients": newJSONResponse(http.StatusCreated, map[string]any{
					"client_id": "client-1",
					"secure_files": map[string]any{
						"identity.pem": base64.StdEncoding.EncodeToString([]byte("certificate")),
					},
				}),
				"GET /databuses/demo/applications/shapes": newJSONResponse(http.StatusOK, map[string]any{
					"client_config":  `<dds path="./secure/identity.pem"/>`,
					"client_example": "# example\n",
					"manifest":       map[string]any{"version": 1, "application": "shapes", "topics": []any{}},
				}),
			}}
			runner := New(api, io.Discard)
			runner.CSRGenerator = func(string, string, string) ([]byte, string, error) {
				return []byte("private key"), "csr", nil
			}
			if err := runner.RegisterAppClientWithOptions("demo", "shapes", "client-1", "", true, false, test.zip); err != nil {
				t.Fatal(err)
			}
			root := "demo-shapes-client-1"
			if test.zip {
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("ZIP output must not create a directory; stat error: %v", err)
				}
				archive, err := zip.OpenReader(root + ".zip")
				if err != nil {
					t.Fatal(err)
				}
				defer archive.Close()
				files := map[string]*zip.File{}
				for _, file := range archive.File {
					files[file.Name] = file
				}
				for name, want := range map[string]string{
					"shapes.xml":          `<dds path="./secure/identity.pem"/>`,
					"shapes.py":           "# example\n",
					"secure/identity.pem": "certificate",
					"secure/client.key":   "private key",
				} {
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
				if files[root+"/manifest.json"] == nil || len(files) != 5 {
					t.Fatalf("unexpected ZIP contents: %#v", files)
				}
				if mode := files[root+"/secure/client.key"].Mode().Perm(); mode != 0o600 {
					t.Fatalf("private key mode = %#o, want 0600", mode)
				}
				return
			}
			if _, err := os.Stat(root + ".zip"); !os.IsNotExist(err) {
				t.Fatalf("directory output must not create a ZIP; stat error: %v", err)
			}
			for _, name := range []string{"shapes.xml", "shapes.py", "manifest.json", "secure/identity.pem", "secure/client.key"} {
				if _, err := os.Stat(filepath.Join(root, name)); err != nil {
					t.Fatalf("missing %s: %v", name, err)
				}
			}
			keyInfo, err := os.Stat(filepath.Join(root, "secure/client.key"))
			if err != nil {
				t.Fatal(err)
			}
			if keyInfo.Mode().Perm() != 0o600 {
				t.Fatalf("private key mode = %#o, want 0600", keyInfo.Mode().Perm())
			}
		})
	}
}
