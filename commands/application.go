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
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/rtipaths"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type applicationManifest struct {
	Port      json.RawMessage `json:"port"`
	Kind      string          `json:"kind"`
	TopicData json.RawMessage `json:"topic_data"`
}

// applicationBundleV2 separates managed configuration from editable examples.
type applicationBundleV2 struct {
	Version     int    `json:"version"`
	QoS         string `json:"qos"`
	Types       string `json:"types"`
	Application string `json:"application"`
	Example     string `json:"example"`
	Readme      string `json:"readme"`
}

func (bundle *applicationBundleV2) validate() error {
	if bundle.Version != 2 {
		return fmt.Errorf("unsupported application bundle version %d", bundle.Version)
	}
	for _, content := range []string{bundle.QoS, bundle.Types, bundle.Application, bundle.Example, bundle.Readme} {
		if strings.TrimSpace(content) == "" {
			return fmt.Errorf("application bundle artifacts are incomplete")
		}
	}
	return nil
}

func (bundle *applicationBundleV2) files(appName string) map[string][]byte {
	return map[string][]byte{
		appName + ".xml":                      []byte(bundle.Application),
		appName + ".py":                       []byte(bundle.Example),
		"README.md":                           []byte(bundle.Readme),
		"generated/" + appName + ".qos.xml":   []byte(bundle.QoS),
		"generated/" + appName + ".types.xml": []byte(bundle.Types),
	}
}

type applicationArtifacts struct {
	Bundle       *applicationBundleV2 `json:"bundle"`
	ClientConfig string               `json:"client_config"`
	ClientData   json.RawMessage      `json:"client_data"`
	Manifest     json.RawMessage      `json:"manifest"`
}

func (artifacts applicationArtifacts) validateComplete() error {
	if artifacts.Bundle == nil {
		return fmt.Errorf("manager did not return an application bundle")
	}
	if err := artifacts.Bundle.validate(); err != nil {
		return err
	}
	manifest := bytes.TrimSpace(artifacts.Manifest)
	if len(manifest) == 0 || bytes.Equal(manifest, []byte("null")) {
		return fmt.Errorf("application artifacts are incomplete")
	}
	return nil
}

func validArtifactName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\:\x00")
}

func validateApplicationNames(databusName, appName, clientID string) error {
	names := []struct{ label, value string }{{"Databus name", databusName}, {"application name", appName}}
	if clientID != "" {
		names = append(names, struct{ label, value string }{"client ID", clientID})
	}
	for _, name := range names {
		if !validArtifactName(name.value) {
			return fmt.Errorf("invalid %s %q: expected a name without path components", name.label, name.value)
		}
	}
	return nil
}

func validateSecureFileNames(secureFiles map[string]string) error {
	for name := range secureFiles {
		if !validArtifactName(name) {
			return fmt.Errorf("invalid secure filename %q: expected a filename without path components", name)
		}
	}
	return nil
}

func applicationBundle(databusName string, appName string, clientID string, artifacts applicationArtifacts, secureFiles map[string]string, privateKey []byte) ([]byte, error) {
	if err := artifacts.validateComplete(); err != nil {
		return nil, err
	}
	if err := validateSecureFileNames(secureFiles); err != nil {
		return nil, err
	}
	if err := validateApplicationNames(databusName, appName, clientID); err != nil {
		return nil, err
	}
	baseName := databusName + "-" + appName
	if clientID != "" {
		baseName += "-" + clientID
	}

	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	addFile := func(name string, data []byte, mode os.FileMode) error {
		header := &zip.FileHeader{Name: filepath.ToSlash(filepath.Join(baseName, name)), Method: zip.Deflate}
		header.SetMode(mode)
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = entry.Write(data)
		return err
	}
	files := artifacts.Bundle.files(appName)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := addFile(name, files[name], 0o644); err != nil {
			return nil, err
		}
	}
	manifest, err := json.MarshalIndent(json.RawMessage(artifacts.Manifest), "", "  ")
	if err != nil {
		return nil, err
	}
	if err := addFile("manifest.json", append(manifest, '\n'), 0o644); err != nil {
		return nil, err
	}
	secureDir := "generated/security"
	secureFileNames := make([]string, 0, len(secureFiles))
	for name := range secureFiles {
		secureFileNames = append(secureFileNames, name)
	}
	sort.Strings(secureFileNames)
	for _, name := range secureFileNames {
		if name == "client.key" && len(privateKey) > 0 {
			continue
		}
		encoded := secureFiles[name]
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(name, ".key") {
			mode = 0o600
		}
		if err := addFile(filepath.Join(secureDir, name), decoded, mode); err != nil {
			return nil, err
		}
	}
	if len(privateKey) > 0 {
		if err := addFile(filepath.Join(secureDir, "client.key"), privateKey, 0o600); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type ApplicationDownloadOptions struct {
	QoS             bool
	Types           bool
	Generated       bool
	GenerateExample bool
	IncludeManifest bool
	ZIP             bool
	ForceOverwrite  bool
	TargetDir       string
	ConfigOutput    string
}

func (runner *Runner) fetchApplication(name string, appName string) (applicationArtifacts, bool, error) {
	response, err := runner.API.Get("/databuses/" + name + "/applications/" + appName)
	if err != nil {
		return applicationArtifacts{}, false, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return applicationArtifacts{}, false, nil
	}
	var artifacts applicationArtifacts
	if err := json.Unmarshal(body, &artifacts); err != nil {
		return applicationArtifacts{}, false, err
	}
	return artifacts, true, nil
}

func formatJSON(raw json.RawMessage) ([]byte, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (runner *Runner) warnUnknownTypes(rawManifest json.RawMessage) {
	var manifest struct {
		Topics []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"topics"`
	}
	if json.Unmarshal(rawManifest, &manifest) != nil {
		return
	}
	unknown := make([]string, 0)
	for _, topic := range manifest.Topics {
		if topic.Type == "unknown" {
			unknown = append(unknown, topic.Name)
		}
	}
	if len(unknown) > 0 {
		_, _ = fmt.Fprintf(runner.Out, "Warning: Type information is unavailable for: %s\n", strings.Join(unknown, ", "))
	}
}

func parseApplicationPort(rawPort json.RawMessage) (int, bool, error) {
	if len(rawPort) == 0 || string(rawPort) == "null" {
		return 0, false, nil
	}
	var port int
	if err := json.Unmarshal(rawPort, &port); err == nil {
		return port, true, nil
	}
	var portText string
	if err := json.Unmarshal(rawPort, &portText); err != nil {
		return 0, false, fmt.Errorf("port must be a number or numeric string")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, false, fmt.Errorf("port must be a number or numeric string")
	}
	return port, true, nil
}

func (runner *Runner) CreateApplication(name string, appName string, port int, kind string, configFile string, portOverridden bool) error {
	var topicData map[string]any
	if configFile != "" {
		data, err := runner.ReadFile(configFile)
		if err != nil {
			return invalidInput(fmt.Sprintf("read application configuration: %v", err), err)
		}
		var manifest *applicationManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return invalidInput(fmt.Sprintf("parse application configuration: %v", err), err)
		}
		if manifest == nil {
			return invalidInput("application configuration must be a JSON object, not null", nil)
		}
		kind = manifest.Kind
		manifestPort, hasManifestPort, err := parseApplicationPort(manifest.Port)
		if err != nil {
			return invalidInput(fmt.Sprintf("parse application configuration: %v", err), err)
		}
		if hasManifestPort && !portOverridden {
			port = manifestPort
		}
		topicData = map[string]any{}
		if len(manifest.TopicData) > 0 {
			if err := json.Unmarshal(manifest.TopicData, &topicData); err != nil || topicData == nil {
				return invalidInput("application configuration topic_data must be a JSON object", err)
			}
		}
	}
	if kind == "" {
		kind = "app"
	}
	if kind == "observability-collector" {
		kind = "telemetry-service-collector"
	}
	if kind != "app" && kind != "gateway" && kind != "telemetry-service-collector" {
		return invalidInput(fmt.Sprintf("invalid application kind %q; expected app, gateway, or observability-collector", kind), nil)
	}
	payload := map[string]any{"port": port, "kind": kind}
	if appName != "" {
		payload["client_name"] = appName
	}
	if configFile != "" {
		payload["topic_data"] = topicData
	}
	response, err := runner.API.Post("/databuses/"+url.PathEscape(name)+"/applications", payload)
	if err != nil {
		return err
	}
	body, err := readResponse(response, http.StatusCreated)
	if err != nil {
		return err
	}
	if runner.JSON {
		var resource any
		if len(bytes.TrimSpace(body)) != 0 {
			if err := json.Unmarshal(body, &resource); err != nil {
				return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned invalid application JSON: " + err.Error(), Cause: err}
			}
			if resource == nil {
				return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned null instead of an application result"}
			}
		}
		return runner.writeResult(map[string]any{"databus": name, "application": appName, "kind": kind, "port": port, "status": "created", "resource": resource})
	}
	return runner.printResponseBody(body)
}

func (runner *Runner) DownloadApplication(name string, appName string, options ApplicationDownloadOptions) error {
	if err := validateApplicationNames(name, appName, ""); err != nil {
		return err
	}
	artifacts, found, err := runner.fetchApplication(name, appName)
	if err != nil || !found {
		return err
	}
	if options.ConfigOutput != "" {
		var clientData map[string]any
		if json.Unmarshal(artifacts.ClientData, &clientData) != nil || clientData == nil {
			return fmt.Errorf("unexpected application configuration for %q", appName)
		}
		topicData := map[string]any{}
		if rawTopics, exists := clientData["topics"]; exists {
			var topicsOK bool
			topicData, topicsOK = rawTopics.(map[string]any)
			if !topicsOK || topicData == nil {
				return fmt.Errorf("unexpected topic data for application %q", appName)
			}
		}
		manifest := map[string]any{"topic_data": topicData}
		if kind, ok := clientData["kind"].(string); ok && kind != "" {
			manifest["kind"] = kind
		}
		if rawPort, exists := clientData["port"]; exists {
			encodedPort, err := json.Marshal(rawPort)
			if err != nil {
				return err
			}
			port, hasPort, err := parseApplicationPort(encodedPort)
			if err != nil {
				return fmt.Errorf("unexpected port for application %q", appName)
			}
			if hasPort {
				manifest["port"] = port
			}
		}
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		_, err = runner.SaveClientFile("", options.ConfigOutput, append(data, '\n'), options.ForceOverwrite)
		return err
	}
	var clientData struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(artifacts.ClientData, &clientData)
	if clientData.Kind == "gateway" || clientData.Kind == "telemetry-service-collector" {
		if options.ZIP || options.GenerateExample || options.QoS || options.Types || options.Generated || options.IncludeManifest {
			return fmt.Errorf("artifact flags are only supported for cloud applications")
		}
		if strings.TrimSpace(artifacts.ClientConfig) == "" {
			return fmt.Errorf("manager did not return XML configuration for %q", appName)
		}
		saved, err := runner.SaveClientFile(options.TargetDir, appName+".xml", []byte(artifacts.ClientConfig), options.ForceOverwrite)
		if err != nil {
			return err
		}
		if !saved {
			return fmt.Errorf("application configuration was not saved")
		}
		return nil
	}
	if err := artifacts.validateComplete(); err != nil {
		return err
	}
	if options.ZIP {
		fileName := name + "-" + appName + ".zip"
		if err := validateApplicationOutputPath(filepath.Clean(options.TargetDir), filepath.Join(options.TargetDir, fileName), false); err != nil {
			return err
		}
		bundle, err := applicationBundle(name, appName, "", artifacts, nil, nil)
		if err != nil {
			return err
		}
		saved, err := runner.SaveClientFile(options.TargetDir, fileName, bundle, options.ForceOverwrite)
		if err != nil {
			return err
		}
		if !saved {
			return fmt.Errorf("application archive was not saved")
		}
		runner.warnUnknownTypes(artifacts.Manifest)
		return nil
	}
	return runner.downloadSplitApplication(appName, artifacts, options)
}

// Refresh only the selected managed files. Existing editable files are skipped
// unless the caller explicitly requests replacement with --force.
func (runner *Runner) downloadSplitApplication(appName string, artifacts applicationArtifacts, options ApplicationDownloadOptions) error {
	if err := artifacts.Bundle.validate(); err != nil {
		return err
	}
	files := map[string][]byte{}
	allGenerated := options.Generated || options.GenerateExample || (!options.QoS && !options.Types)
	if allGenerated || options.QoS {
		files["generated/"+appName+".qos.xml"] = []byte(artifacts.Bundle.QoS)
	}
	if allGenerated || options.Types {
		files["generated/"+appName+".types.xml"] = []byte(artifacts.Bundle.Types)
	}
	if options.GenerateExample {
		files = artifacts.Bundle.files(appName)
	}
	if options.IncludeManifest || options.GenerateExample {
		raw := bytes.TrimSpace(artifacts.Manifest)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return fmt.Errorf("manager did not return manifest")
		}
		data, err := formatJSON(raw)
		if err != nil {
			return err
		}
		files["manifest.json"] = data
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	// Check all destinations before writing any files.
	root := filepath.Clean(options.TargetDir)
	selected := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(options.TargetDir, name)
		if err := validateApplicationOutputPath(root, path, false); err != nil {
			return err
		}
		if _, err := runner.Stat(path); err == nil {
			if !strings.HasPrefix(name, "generated/") && name != "manifest.json" && !options.ForceOverwrite {
				_, _ = fmt.Fprintf(runner.Out, "Preserved %s (use --force to replace examples)\n", path)
				continue
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect application output %s: %w", path, err)
		}
		selected = append(selected, name)
	}
	for _, name := range selected {
		if err := validateApplicationOutputPath(root, filepath.Join(root, name), false); err != nil {
			return err
		}
		saved, err := runner.SaveClientFile(options.TargetDir, name, files[name], true)
		if err != nil {
			return err
		}
		if !saved {
			return fmt.Errorf("application artifact %s was not saved", name)
		}
	}
	runner.warnUnknownTypes(artifacts.Manifest)
	return nil
}

func (runner *Runner) GetApplication(name string, appName string, generateExample bool, forceOverwrite bool, targetDir string, manifestOutput string) error {
	return runner.DownloadApplication(name, appName, ApplicationDownloadOptions{
		GenerateExample: generateExample,
		ForceOverwrite:  forceOverwrite,
		TargetDir:       targetDir,
		ConfigOutput:    manifestOutput,
	})
}

func (runner *Runner) DeleteApplication(name string, appName string) error {
	response, err := runner.API.Delete("/databuses/" + url.PathEscape(name) + "/applications/" + url.PathEscape(appName))
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"databus": name, "application": appName, "status": "deleted"}, fmt.Sprintf("Application '%s' successfully deleted from databus '%s'", appName, name))
}

func (runner *Runner) ListAppClients(name string, appName string) error {
	response, err := runner.API.Get("/databuses/" + url.PathEscape(name) + "/applications/" + url.PathEscape(appName) + "/clients")
	if err != nil {
		return err
	}
	body, err := readResponse(response, http.StatusOK)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned invalid client JSON: " + err.Error(), Cause: err}
	}
	clients, ok := payload["clients"]
	if !ok || clients == nil {
		return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API response is missing the clients collection"}
	}
	switch clients.(type) {
	case map[string]any, []any:
	default:
		return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API clients must be an object or array"}
	}
	if runner.JSON {
		return runner.writeResult(map[string]any{"databus": name, "application": appName, "clients": clients})
	}
	return runner.writeResult(clients)
}

func (runner *Runner) SaveClientFile(targetDir string, fileName string, data []byte, forceOverwrite bool) (bool, error) {
	return runner.saveClientFile(targetDir, fileName, data, forceOverwrite, strings.HasSuffix(fileName, ".key"))
}

func (runner *Runner) saveClientFile(targetDir string, fileName string, data []byte, forceOverwrite bool, sensitive bool) (bool, error) {
	filePath := fileName
	if targetDir != "" {
		filePath = filepath.Join(targetDir, fileName)
	}
	if _, err := runner.Stat(filePath); err == nil {
		if !forceOverwrite {
			_, _ = fmt.Fprintf(runner.Out, "%s already exists. Use -f to overwrite.\n", filePath)
			return false, nil
		}
	}
	if err := runner.writeOutputFile(filePath, data, sensitive); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(runner.Out, "Saved %s\n", filePath)
	return true, nil
}

func (runner *Runner) SaveSecureFiles(secureFiles map[string]string, privateKey []byte, forceOverwrite bool, targetDir string) error {
	if err := validateSecureFileNames(secureFiles); err != nil {
		return err
	}
	for filename, encoded := range secureFiles {
		if filename == "client.key" && len(privateKey) > 0 {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return err
		}
		if _, err := runner.SaveClientFile(targetDir, filename, decoded, forceOverwrite); err != nil {
			return err
		}
	}
	if len(privateKey) > 0 {
		if _, err := runner.SaveClientFile(targetDir, "client.key", privateKey, forceOverwrite); err != nil {
			return err
		}
	}
	return nil
}

// Validate the path and expected destination type without creating it.
func validateApplicationOutputPath(root, path string, directory bool) error {
	if err := rtipaths.ValidateManagedPath(root, path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if directory {
		if !info.IsDir() {
			return fmt.Errorf("application output must be a directory: %s", path)
		}
	} else if !info.Mode().IsRegular() {
		return fmt.Errorf("application output must be a regular file: %s", path)
	}
	return nil
}

// Validate all existing bundle destinations before creating client credentials.
func validateClientBundlePaths(destination, appName string, bundle *applicationBundleV2) error {
	paths := []string{"manifest.json"}
	for name := range bundle.files(appName) {
		paths = append(paths, name)
	}
	for _, name := range paths {
		if err := validateApplicationOutputPath(destination, filepath.Join(destination, name), false); err != nil {
			return err
		}
	}
	securityDir := filepath.Join(destination, "generated", "security")
	if err := validateApplicationOutputPath(destination, securityDir, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(securityDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// Security filenames come from registration, so check existing entries too.
	for _, entry := range entries {
		if err := validateApplicationOutputPath(destination, filepath.Join(securityDir, entry.Name()), false); err != nil {
			return err
		}
	}
	return nil
}

type ApplicationRegistrationOptions struct {
	CSRFile            string
	TargetDir          string
	GeneratePrivateKey bool
	ForceOverwrite     bool
	ZIP                bool
}

func (runner *Runner) RegisterAppClientWithOptions(name, appName, clientID string, options ApplicationRegistrationOptions) error {
	if clientID == "" {
		_, _ = fmt.Fprintln(runner.Out, "Error: --client-id is required")
		return nil
	}
	if err := validateApplicationNames(name, appName, clientID); err != nil {
		return err
	}
	destination := name + "-" + appName + "-" + clientID
	root := "."
	if options.TargetDir != "" {
		root = filepath.Clean(options.TargetDir)
	}
	if options.ZIP {
		destination = filepath.Join(root, destination+".zip")
	} else if options.TargetDir != "" {
		destination = root
	}
	if err := validateApplicationOutputPath(root, destination, !options.ZIP); err != nil {
		return err
	}
	if _, err := runner.Stat(destination); err == nil {
		if !options.ForceOverwrite {
			return fmt.Errorf("%s already exists. Use --force to overwrite", destination)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect client output %s: %w", destination, err)
	}
	artifacts, found, err := runner.fetchApplication(name, appName)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("cannot register client %q: application artifacts are unavailable", clientID)
	}
	if err := artifacts.validateComplete(); err != nil {
		return err
	}
	if !options.ZIP {
		if err := validateClientBundlePaths(destination, appName, artifacts.Bundle); err != nil {
			return err
		}
	}
	manifest, err := formatJSON(artifacts.Manifest)
	if err != nil {
		return err
	}
	var privateKey []byte
	var csrPEM string
	if options.GeneratePrivateKey {
		if runner.CSRGenerator == nil {
			return fmt.Errorf("CSR generator is not configured")
		}
		var err error
		privateKey, csrPEM, err = runner.CSRGenerator(name, appName, clientID)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(runner.Out, "Generated private key and CSR.")
	} else {
		if options.CSRFile == "" {
			_, _ = fmt.Fprintln(runner.Out, "Error: either --csr-file or --gen-private-key is required")
			return nil
		}
		data, err := runner.ReadFile(options.CSRFile)
		if err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Error reading CSR file: %v\n", err)
			return nil
		}
		csrPEM = string(data)
	}
	response, err := runner.API.Post("/databuses/"+name+"/applications/"+appName+"/clients", map[string]any{"client_id": clientID, "csr": csrPEM})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	secureFiles := map[string]string{}
	if rawSecureFiles, ok := payload["secure_files"].(map[string]any); ok {
		for key, value := range rawSecureFiles {
			if text, ok := value.(string); ok {
				secureFiles[key] = text
			}
		}
	}
	delete(payload, "secure_files")
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	if err := validateSecureFileNames(secureFiles); err != nil {
		return err
	}
	if options.ZIP {
		bundle, err := applicationBundle(name, appName, clientID, artifacts, secureFiles, privateKey)
		if err != nil {
			return err
		}
		sensitive := len(privateKey) > 0
		for name := range secureFiles {
			sensitive = sensitive || strings.HasSuffix(name, ".key")
		}
		saved, err := runner.saveClientFile("", destination, bundle, options.ForceOverwrite, sensitive)
		if err != nil {
			return err
		}
		if !saved {
			return fmt.Errorf("client %q was registered, but archive %s was not saved", clientID, destination)
		}
		runner.warnUnknownTypes(artifacts.Manifest)
		return nil
	}
	if err := runner.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	files := artifacts.Bundle.files(appName)
	files["manifest.json"] = manifest
	for fileName, data := range files {
		if _, err := runner.SaveClientFile(destination, fileName, data, options.ForceOverwrite); err != nil {
			return err
		}
	}
	secureDir := filepath.Join(destination, "generated", "security")
	if err := runner.SaveSecureFiles(secureFiles, privateKey, options.ForceOverwrite, secureDir); err != nil {
		return err
	}
	runner.warnUnknownTypes(artifacts.Manifest)
	return nil
}

func (runner *Runner) RegisterAppClient(name string, appName string, clientID string, csrFile string, genPrivateKey bool, forceOverwrite bool) error {
	return runner.RegisterAppClientWithOptions(name, appName, clientID, ApplicationRegistrationOptions{CSRFile: csrFile, GeneratePrivateKey: genPrivateKey, ForceOverwrite: forceOverwrite})
}

func (runner *Runner) RevokeAppClient(name string, appName string, clientID string) error {
	if clientID == "" {
		return invalidInput("--client-id is required for revoke", nil)
	}
	response, err := runner.API.Delete("/databuses/" + url.PathEscape(name) + "/applications/" + url.PathEscape(appName) + "/clients/" + url.PathEscape(clientID))
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"databus": name, "application": appName, "client_id": clientID, "status": "revoked"}, fmt.Sprintf("Client '%s' revoked successfully.", clientID))
}
