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

type applicationArtifacts struct {
	ClientConfig  string          `json:"client_config"`
	ClientData    json.RawMessage `json:"client_data"`
	ClientExample string          `json:"client_example"`
	Manifest      json.RawMessage `json:"manifest"`
}

func (artifacts applicationArtifacts) validateComplete() error {
	manifest := bytes.TrimSpace(artifacts.Manifest)
	if artifacts.ClientConfig == "" || artifacts.ClientExample == "" || len(manifest) == 0 || bytes.Equal(manifest, []byte("null")) {
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
	if err := addFile(appName+".xml", []byte(artifacts.ClientConfig), 0o644); err != nil {
		return nil, err
	}
	if err := addFile(appName+".py", []byte(artifacts.ClientExample), 0o644); err != nil {
		return nil, err
	}
	manifest, err := json.MarshalIndent(json.RawMessage(artifacts.Manifest), "", "  ")
	if err != nil {
		return nil, err
	}
	if err := addFile("manifest.json", append(manifest, '\n'), 0o644); err != nil {
		return nil, err
	}
	secureDir := ""
	if strings.Contains(artifacts.ClientConfig, "./secure/") {
		secureDir = "secure"
	}
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
			return fmt.Errorf("read application configuration: %w", err)
		}
		var manifest applicationManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return fmt.Errorf("parse application configuration: %w", err)
		}
		kind = manifest.Kind
		manifestPort, hasManifestPort, err := parseApplicationPort(manifest.Port)
		if err != nil {
			return fmt.Errorf("parse application configuration: %w", err)
		}
		if hasManifestPort && !portOverridden {
			port = manifestPort
		}
		topicData = map[string]any{}
		if len(manifest.TopicData) > 0 {
			if err := json.Unmarshal(manifest.TopicData, &topicData); err != nil || topicData == nil {
				return fmt.Errorf("application configuration topic_data must be a JSON object")
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
		return fmt.Errorf("invalid application kind %q; expected app, gateway, or observability-collector", kind)
	}
	payload := map[string]any{"port": port, "kind": kind}
	if appName != "" {
		payload["client_name"] = appName
	}
	if configFile != "" {
		payload["topic_data"] = topicData
	}
	response, err := runner.API.Post("/databuses/"+name+"/applications", payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusCreated {
		runner.printResponseBody(body)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
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
	if artifacts.ClientConfig == "" {
		return fmt.Errorf("manager did not return XML configuration for %q", appName)
	}
	if options.ZIP {
		bundle, err := applicationBundle(name, appName, "", artifacts, nil, nil)
		if err != nil {
			return err
		}
		if _, err := runner.SaveClientFile(options.TargetDir, name+"-"+appName+".zip", bundle, options.ForceOverwrite); err != nil {
			return err
		}
		runner.warnUnknownTypes(artifacts.Manifest)
		return nil
	}
	if options.GenerateExample && artifacts.ClientExample == "" {
		return fmt.Errorf("manager did not return example for %q", appName)
	}
	var manifest []byte
	if options.IncludeManifest {
		raw := bytes.TrimSpace(artifacts.Manifest)
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return fmt.Errorf("manager did not return manifest")
		}
		manifest, err = formatJSON(raw)
		if err != nil {
			return err
		}
	}
	type outputFile struct {
		name string
		data []byte
	}
	files := []outputFile{{appName + ".xml", []byte(artifacts.ClientConfig)}}
	if options.GenerateExample {
		files = append(files, outputFile{appName + ".py", []byte(artifacts.ClientExample)})
	}
	if options.IncludeManifest {
		files = append(files, outputFile{"manifest.json", manifest})
	}
	for _, file := range files {
		path := filepath.Join(options.TargetDir, file.name)
		if info, err := runner.Stat(path); err == nil {
			if info != nil && info.IsDir() {
				return fmt.Errorf("%s is a directory", path)
			}
			if !options.ForceOverwrite {
				return fmt.Errorf("%s already exists. Use --force to overwrite", path)
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect application output %s: %w", path, err)
		}
	}
	for _, file := range files {
		saved, err := runner.SaveClientFile(options.TargetDir, file.name, file.data, options.ForceOverwrite)
		if err != nil {
			return err
		}
		if !saved {
			return fmt.Errorf("application artifact %s was not saved", filepath.Join(options.TargetDir, file.name))
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
	response, err := runner.API.Delete("/databuses/" + name + "/applications/" + appName)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Application '%s' successfully deleted from databus '%s'\n", appName, name)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) ListAppClients(name string, appName string) error {
	response, err := runner.API.Get("/databuses/" + name + "/applications/" + appName + "/clients")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		formatted, _ := json.MarshalIndent(payload["clients"], "", "  ")
		_, _ = fmt.Fprintln(runner.Out, string(formatted))
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

func CreateClientBundleDirectory(databusName string, appName string, clientName string) (string, error) {
	if databusName == "" || appName == "" || clientName == "" {
		return "", fmt.Errorf("databus_name, app_name, and client_name must be provided")
	}
	if err := validateApplicationNames(databusName, appName, clientName); err != nil {
		return "", err
	}
	targetDir := fmt.Sprintf("%s-%s-%s", databusName, appName, clientName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	return targetDir, nil
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

func (runner *Runner) RegisterAppClientWithOptions(name string, appName string, clientID string, csrFile string, genPrivateKey bool, forceOverwrite bool, zipOutput bool) error {
	if clientID == "" {
		_, _ = fmt.Fprintln(runner.Out, "Error: --client-id is required")
		return nil
	}
	if err := validateApplicationNames(name, appName, clientID); err != nil {
		return err
	}
	destination := name + "-" + appName + "-" + clientID
	if zipOutput {
		destination += ".zip"
	}
	if info, err := runner.Stat(destination); err == nil {
		if zipOutput == info.IsDir() {
			return fmt.Errorf("%s has the wrong output type", destination)
		}
		if !forceOverwrite {
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
	manifest, err := formatJSON(artifacts.Manifest)
	if err != nil {
		return err
	}
	var privateKey []byte
	var csrPEM string
	if genPrivateKey {
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
		if csrFile == "" {
			_, _ = fmt.Fprintln(runner.Out, "Error: either --csr-file or --gen-private-key is required")
			return nil
		}
		data, err := runner.ReadFile(csrFile)
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
	if zipOutput {
		bundle, err := applicationBundle(name, appName, clientID, artifacts, secureFiles, privateKey)
		if err != nil {
			return err
		}
		sensitive := len(privateKey) > 0
		for name := range secureFiles {
			sensitive = sensitive || strings.HasSuffix(name, ".key")
		}
		saved, err := runner.saveClientFile("", destination, bundle, forceOverwrite, sensitive)
		if err != nil {
			return err
		}
		if !saved {
			return fmt.Errorf("client %q was registered, but archive %s was not saved", clientID, destination)
		}
		runner.warnUnknownTypes(artifacts.Manifest)
		return nil
	}
	targetDir, err := CreateClientBundleDirectory(name, appName, clientID)
	if err != nil {
		return err
	}
	files := map[string][]byte{
		appName + ".xml": []byte(artifacts.ClientConfig),
		appName + ".py":  []byte(artifacts.ClientExample),
		"manifest.json":  manifest,
	}
	for fileName, data := range files {
		if _, err := runner.SaveClientFile(targetDir, fileName, data, forceOverwrite); err != nil {
			return err
		}
	}
	secureDir := targetDir
	if strings.Contains(artifacts.ClientConfig, "./secure/") {
		secureDir = filepath.Join(targetDir, "secure")
	}
	if err := runner.SaveSecureFiles(secureFiles, privateKey, forceOverwrite, secureDir); err != nil {
		return err
	}
	runner.warnUnknownTypes(artifacts.Manifest)
	return nil
}

func (runner *Runner) RegisterAppClient(name string, appName string, clientID string, csrFile string, genPrivateKey bool, forceOverwrite bool) error {
	return runner.RegisterAppClientWithOptions(name, appName, clientID, csrFile, genPrivateKey, forceOverwrite, false)
}

func (runner *Runner) RevokeAppClient(name string, appName string, clientID string) error {
	if clientID == "" {
		_, _ = fmt.Fprintln(runner.Out, "Error: --client-id is required for revoke")
		return nil
	}
	response, err := runner.API.Delete("/databuses/" + name + "/applications/" + appName + "/clients/" + clientID)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNoContent {
		_, _ = fmt.Fprintf(runner.Out, "Client '%s' revoked successfully.\n", clientID)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}
