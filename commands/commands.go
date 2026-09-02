// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/edgestore"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

const (
	databusStatusPollInterval = 5 * time.Second
	databusStatusWaitTimeout  = 10 * time.Minute
)

type API interface {
	Get(path string) (*http.Response, error)
	Post(path string, payload any) (*http.Response, error)
	PostWithBearerToken(path string, payload any, bearerToken string) (*http.Response, error)
	Patch(path string, payload any) (*http.Response, error)
	Delete(path string) (*http.Response, error)
}

type CSRGenerator func(databus string, app string, clientID string) ([]byte, string, error)

type Runner struct {
	API          API
	Out          io.Writer
	Sleep        func(time.Duration)
	ReadFile     func(string) ([]byte, error)
	WriteFile    func(string, []byte, os.FileMode) error
	MkdirAll     func(string, os.FileMode) error
	Stat         func(string) (os.FileInfo, error)
	CSRGenerator CSRGenerator
	EdgeStore    *edgestore.Store
}

func New(api API, out io.Writer) *Runner {
	return &Runner{
		API:       api,
		Out:       out,
		Sleep:     time.Sleep,
		ReadFile:  os.ReadFile,
		WriteFile: os.WriteFile,
		MkdirAll:  os.MkdirAll,
		Stat:      os.Stat,
	}
}

func (runner *Runner) printResponseError(prefix string, statusCode int, body []byte) {
	_, _ = fmt.Fprintf(runner.Out, "%s%s\n", prefix, httputil.FormatError(statusCode, body))
}

func (runner *Runner) printResponseBody(body []byte) {
	var payload any
	if json.Unmarshal(body, &payload) == nil {
		body, _ = json.MarshalIndent(payload, "", "  ")
	}
	_, _ = fmt.Fprintln(runner.Out, string(body))
}

func (runner *Runner) writeOutputFile(filePath string, data []byte, sensitive bool) error {
	fileMode := os.FileMode(0o644)
	dirMode := os.FileMode(0o755)
	if sensitive {
		fileMode = 0o600
		dirMode = 0o700
	}
	if dir := filepath.Dir(filePath); dir != "." {
		if err := runner.MkdirAll(dir, dirMode); err != nil {
			return err
		}
	}
	return runner.WriteFile(filePath, data, fileMode)
}

func (runner *Runner) queryDatabusStatus(name string) (string, bool, error) {
	response, err := runner.API.Get("/databuses/" + name)
	if err != nil {
		return "", false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", false, nil
	}
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return "", true, err
	}
	status, _ := payload["status"].(string)
	return status, true, nil
}

func (runner *Runner) waitForDatabusStatusChange(name string, previousStatus string) (string, bool, error) {
	waited := time.Duration(0)
	for {
		runner.Sleep(databusStatusPollInterval)
		waited += databusStatusPollInterval
		status, exists, err := runner.queryDatabusStatus(name)
		if err != nil || !exists {
			return status, exists, err
		}
		if status != previousStatus {
			return status, exists, nil
		}
		if waited >= databusStatusWaitTimeout {
			return status, exists, fmt.Errorf("timed out waiting for databus %q to leave %q after %s", name, previousStatus, databusStatusWaitTimeout)
		}
	}
}

func (runner *Runner) ListDatabuses(short bool) error {
	path := "/databuses"
	if !short {
		path += "?extra_fields=true"
	}
	response, err := runner.API.Get(path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if short {
		resources, _ := payload["databuses"].(map[string]any)
		for name, rawInfo := range resources {
			kind := "databus"
			if info, ok := rawInfo.(map[string]any); ok {
				if value, ok := info["kind"].(string); ok && value != "" {
					kind = value
				}
			}
			_, _ = fmt.Fprintf(runner.Out, "- %s (%s)\n", name, kind)
		}
		return nil
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) QueryDatabus(name string) error {
	response, err := runner.API.Get("/databuses/" + name)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		formatted, _ := json.MarshalIndent(payload, "", "  ")
		_, _ = fmt.Fprintln(runner.Out, string(formatted))
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) CreateDatabus(name string, replicas int, observabilityServiceName string, networkName string, secure bool) error {
	payload := map[string]any{"name": name, "replicas": replicas, "network_name": networkName, "secure": secure}
	if observabilityServiceName != "" {
		payload["observability_service_name"] = observabilityServiceName
	}
	response, err := runner.API.Post("/databuses", payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	_, _ = fmt.Fprintln(runner.Out, "Databus creation started successfully.")
	_, _ = fmt.Fprintln(runner.Out, "Waiting for creation to complete... (safe to Ctrl+C)")
	status, exists, err := runner.waitForDatabusStatusChange(name, "creating")
	if err != nil {
		return err
	}
	if !exists {
		_, _ = fmt.Fprintln(runner.Out, "Failed to get databus status")
	} else {
		_, _ = fmt.Fprintf(runner.Out, "Databus status:  %s\n", status)
	}
	return nil
}

func (runner *Runner) CreateObsService(name string, networkName string, secure bool) error {
	payload := map[string]any{"name": name, "replicas": 0, "enable_edge_observability": true, "secure": secure}
	if networkName != "" {
		payload["network_name"] = networkName
	}
	response, err := runner.API.Post("/databuses", payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	_, _ = fmt.Fprintln(runner.Out, "Observability Service creation started successfully.")
	_, _ = fmt.Fprintln(runner.Out, "Waiting for creation to complete... (safe to Ctrl+C)")
	status, exists, err := runner.waitForDatabusStatusChange(name, "creating")
	if err != nil {
		return err
	}
	if !exists {
		_, _ = fmt.Fprintln(runner.Out, "Failed to get Observability Service status")
	} else {
		_, _ = fmt.Fprintf(runner.Out, "Observability Service status:  %s\n", status)
	}
	return nil
}

func (runner *Runner) ListObservabilityServices(short bool) error {
	path := "/databuses"
	if !short {
		path += "?extra_fields=true"
	}
	response, err := runner.API.Get(path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	resources, _ := payload["databuses"].(map[string]any)
	observability := map[string]any{}
	for name, rawInfo := range resources {
		info, _ := rawInfo.(map[string]any)
		if kind, _ := info["kind"].(string); kind == "telemetry" {
			observability[name] = info
		}
	}
	if short {
		for name := range observability {
			_, _ = fmt.Fprintf(runner.Out, "- %s\n", name)
		}
		return nil
	}
	formatted, _ := json.MarshalIndent(map[string]any{"observability_services": observability}, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) QueryObservabilityService(name string) error {
	return runner.QueryDatabus(name)
}

func (runner *Runner) DeleteObservabilityService(name string) error {
	response, err := runner.API.Delete("/databuses/" + name)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	_, _ = fmt.Fprintln(runner.Out, "Observability Service deletion started successfully.")
	_, _ = fmt.Fprintln(runner.Out, "Waiting for deletion to complete... (safe to Ctrl+C)")
	status, exists, err := runner.waitForDatabusStatusChange(name, "deleting")
	if err != nil {
		return err
	}
	if !exists {
		_, _ = fmt.Fprintln(runner.Out, "Observability Service has been deleted")
	} else {
		_, _ = fmt.Fprintf(runner.Out, "Unexpected Observability Service status:  %s\n", status)
	}
	return nil
}

func (runner *Runner) UpdateObservabilityLink(name string, observabilityServiceName any) error {
	response, err := runner.API.Patch("/databuses/"+name, map[string]any{"observability_service_name": observabilityServiceName})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		action := "linked"
		if observabilityServiceName == nil || observabilityServiceName == "" {
			action = "unlinked"
		}
		_, _ = fmt.Fprintf(runner.Out, "Observability Service %s for Databus '%s'\n", action, name)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) DeleteDatabus(name string) error {
	response, err := runner.API.Delete("/databuses/" + name)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	_, _ = fmt.Fprintln(runner.Out, "Databus deletion started successfully.")
	_, _ = fmt.Fprintln(runner.Out, "Waiting for databus deletion to complete... (safe to Ctrl+C)")
	status, exists, err := runner.waitForDatabusStatusChange(name, "deleting")
	if err != nil {
		return err
	}
	if !exists {
		_, _ = fmt.Fprintln(runner.Out, "Databus has been deleted")
	} else {
		_, _ = fmt.Fprintf(runner.Out, "Unexpected Databus status:  %s\n", status)
	}
	return nil
}

func (runner *Runner) UpdateDatabusStatus(name string, status string) error {
	response, err := runner.API.Patch("/databuses/"+name, map[string]any{"running_status": status})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Databus '%s' %sd successfully.\n", name, status)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

type applicationManifest struct {
	Port      json.RawMessage `json:"port"`
	Kind      string          `json:"kind"`
	TopicData json.RawMessage `json:"topic_data"`
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

func (runner *Runner) GetApplication(name string, appName string, generateExample bool, forceOverwrite bool, targetDir string, manifestOutput string) error {
	response, err := runner.API.Get("/databuses/" + name + "/applications/" + appName)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if manifestOutput != "" {
		clientData, ok := payload["client_data"].(map[string]any)
		if !ok {
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
		_, err = runner.SaveClientFile("", manifestOutput, append(data, '\n'), forceOverwrite)
		return err
	}
	clientConfig, _ := payload["client_config"].(string)
	if clientConfig == "" {
		_, _ = fmt.Fprintf(runner.Out, "Error: Unexpected application configuration for '%s'\n", appName)
		return nil
	}
	if _, err := runner.SaveClientFile(targetDir, appName+".xml", []byte(clientConfig), forceOverwrite); err != nil {
		return err
	}
	if generateExample {
		if example, ok := payload["client_example"].(string); ok && example != "" {
			_, err := runner.SaveClientFile(targetDir, appName+".py", []byte(example), forceOverwrite)
			if err != nil {
				return err
			}
		}
	}
	return nil
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
	targetDir := fmt.Sprintf("%s-%s-%s", databusName, appName, clientName)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	return targetDir, nil
}

func (runner *Runner) SaveClientFile(targetDir string, fileName string, data []byte, forceOverwrite bool) (bool, error) {
	filePath := fileName
	if targetDir != "" {
		filePath = filepath.Join(targetDir, fileName)
	}
	if _, err := runner.Stat(filePath); err == nil && !forceOverwrite {
		_, _ = fmt.Fprintf(runner.Out, "%s already exists. Use -f to overwrite.\n", filePath)
		return false, nil
	}
	sensitive := strings.HasSuffix(fileName, ".key")
	if err := runner.writeOutputFile(filePath, data, sensitive); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(runner.Out, "Saved %s\n", filePath)
	return true, nil
}

func (runner *Runner) SaveSecureFiles(secureFiles map[string]string, privateKey []byte, forceOverwrite bool, targetDir string) error {
	for filename, encoded := range secureFiles {
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

func (runner *Runner) RegisterAppClient(name string, appName string, clientID string, csrFile string, genPrivateKey bool, forceOverwrite bool) error {
	if clientID == "" {
		_, _ = fmt.Fprintln(runner.Out, "Error: --client-id is required")
		return nil
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
	targetDir, err := CreateClientBundleDirectory(name, appName, clientID)
	if err != nil {
		return err
	}
	if err := runner.GetApplication(name, appName, true, forceOverwrite, targetDir, ""); err != nil {
		return err
	}
	return runner.SaveSecureFiles(secureFiles, privateKey, forceOverwrite, targetDir)
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

func (runner *Runner) ListNetworks() error {
	response, err := runner.API.Get("/networks")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		var payload any
		if err := json.Unmarshal(body, &payload); err != nil {
			return err
		}
		formatted, _ := json.MarshalIndent(payload, "", "  ")
		_, _ = fmt.Fprintln(runner.Out, string(formatted))
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) DeleteNetwork(name string) error {
	response, err := runner.API.Delete("/networks/" + name)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Network '%s' deleted successfully.\n", name)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) UpdateFilters(name string, filterFile string) error {
	data, err := runner.ReadFile(filterFile)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading filter file: %v\n", err)
		return nil
	}
	var filtersData any
	if err := json.Unmarshal(data, &filtersData); err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error: Invalid JSON in file '%s': %v\n", filterFile, err)
		return nil
	}
	if list, ok := filtersData.([]any); ok {
		contentFilters := make([]map[string]any, 0)
		allMatch := true
		for _, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				allMatch = false
				break
			}
			topicName, topicOK := entry["topic_name"].(string)
			topicFilter, filterOK := entry["topic_filter"].(string)
			if !topicOK || !filterOK {
				allMatch = false
				break
			}
			if topicFilter != "" {
				contentFilters = append(contentFilters, map[string]any{"topicName": topicName, "expression": topicFilter})
			}
		}
		if allMatch {
			filtersData = map[string]any{"contentFilters": contentFilters}
			_, _ = fmt.Fprintln(runner.Out, "Converting JSON file.")
		}
	}
	response, err := runner.API.Patch("/databuses/"+name, map[string]any{"persistence_filters": filtersData})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Filters for databus '%s' updated successfully.\n", name)
		return nil
	}
	runner.printResponseError("Error updating filters: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) AddUserToDatabus(name string, email string) error {
	response, err := runner.API.Post("/databuses/"+name+"/users/"+email+"/", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusCreated {
		_, _ = fmt.Fprintf(runner.Out, "User '%s' successfully added to databus '%s'\n", email, name)
		return nil
	}
	runner.printResponseError("Error adding user: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) RemoveUserFromDatabus(name string, email string) error {
	response, err := runner.API.Delete("/databuses/" + name + "/users/" + email + "/")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "User '%s' successfully removed from databus '%s'\n", email, name)
		return nil
	}
	runner.printResponseError("Error removing user: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) GetLicense(expirationDays *int, output string) error {
	if expirationDays != nil {
		if *expirationDays < 0 {
			_, _ = fmt.Fprintln(runner.Out, "Error: expiration-days must be greater than or equal to 0")
			return nil
		}
	}
	body, statusCode, err := runner.requestLicense(expirationDays)
	if err != nil {
		return err
	}
	if statusCode != http.StatusOK {
		runner.printResponseError("Error: ", statusCode, body)
		return nil
	}
	if output == "" {
		runner.printResponseBody(body)
		return nil
	}
	if err := runner.writeOutputFile(output, body, false); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "License saved to %s\n", output)
	return nil
}

func (runner *Runner) DownloadLicense(expirationDays *int) ([]byte, error) {
	body, statusCode, err := runner.requestLicense(expirationDays)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", httputil.FormatError(statusCode, body))
	}
	return body, nil
}

func (runner *Runner) requestLicense(expirationDays *int) ([]byte, int, error) {
	payload := map[string]any{}
	if expirationDays != nil {
		if *expirationDays < 0 {
			return nil, 0, fmt.Errorf("expiration-days must be greater than or equal to 0")
		}
		payload["expiration_days"] = *expirationDays
	}
	response, err := runner.API.Post("/licenses", payload)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return body, response.StatusCode, nil
}

// ── Provisioning Service Management ───────────────────────────────────────────────────

// edgePath builds an Edge System API path, percent-escaping every segment so
// caller-supplied values (serial, template name, campaign ID, etc.) containing
// "/", "..", or spaces cannot alter the path structure. Static literals such as
// "campaigns" are passed as segments too; escaping them is a harmless no-op.
func edgePath(segments ...string) string {
	var sb strings.Builder
	for _, s := range segments {
		sb.WriteByte('/')
		sb.WriteString(url.PathEscape(s))
	}
	return sb.String()
}

// printJSON pretty-prints a JSON response body to runner.Out.
func (runner *Runner) printJSON(body []byte) error {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

// getJSON performs a GET and pretty-prints the response body on HTTP 200.
// Any other status is reported via printResponseError.
func (runner *Runner) getJSON(path string) error {
	response, err := runner.API.Get(path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	return runner.printJSON(body)
}

// postJSON performs a POST and pretty-prints the response body when the
// response status equals successStatus. Any other status is reported via
// printResponseError.
func (runner *Runner) postJSON(path string, payload any, successStatus int) error {
	response, err := runner.API.Post(path, payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != successStatus {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	return runner.printJSON(body)
}

// deleteWithMessage performs a DELETE and prints okMsg on HTTP 200.
// Any other status is reported via printResponseError.
func (runner *Runner) deleteWithMessage(path string, okMsg string) error {
	response, err := runner.API.Delete(path)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	_, _ = fmt.Fprintln(runner.Out, okMsg)
	return nil
}

func (runner *Runner) ListEdgeSystems() error {
	return runner.getJSON(edgePath("edge-systems"))
}

func (runner *Runner) CreateEdgeSystem(name string, description string) error {
	payload := map[string]any{"name": name}
	if description != "" {
		payload["description"] = description
	}
	return runner.postJSON(edgePath("edge-systems"), payload, http.StatusAccepted)
}

func (runner *Runner) QueryEdgeSystem(name string) error {
	return runner.getJSON(edgePath("edge-systems", name))
}

func (runner *Runner) DeleteEdgeSystem(name string) error {
	return runner.deleteWithMessage(edgePath("edge-systems", name),
		fmt.Sprintf("Provisioning Service '%s' deleted successfully.", name))
}

// ── Governance Templates ─────────────────────────────────────────────────────

func (runner *Runner) CreateGovernanceTemplate(edgeSystem string, name string, xmlFile string) error {
	data, err := runner.ReadFile(xmlFile)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading governance XML file: %v\n", err)
		return nil
	}
	payload := map[string]any{"name": name, "xmlContent": string(data)}
	return runner.postJSON(edgePath("edge-systems", edgeSystem, "governance-templates"), payload, http.StatusCreated)
}

func (runner *Runner) ListGovernanceTemplates(edgeSystem string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "governance-templates"))
}

func (runner *Runner) DeleteGovernanceTemplate(edgeSystem string, templateName string) error {
	return runner.deleteWithMessage(edgePath("edge-systems", edgeSystem, "governance-templates", templateName),
		fmt.Sprintf("Governance template '%s' deleted from Provisioning Service '%s'.", templateName, edgeSystem))
}

// ── Permissions Templates ─────────────────────────────────────────────────────

func (runner *Runner) CreatePermissionsTemplate(edgeSystem string, name string, xmlFile string) error {
	data, err := runner.ReadFile(xmlFile)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading permissions XML file: %v\n", err)
		return nil
	}
	payload := map[string]any{"name": name, "xmlContent": string(data)}
	return runner.postJSON(edgePath("edge-systems", edgeSystem, "permissions-templates"), payload, http.StatusCreated)
}

func (runner *Runner) ListPermissionsTemplates(edgeSystem string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "permissions-templates"))
}

func (runner *Runner) GetPermissionsTemplate(edgeSystem string, templateName string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "permissions-templates", templateName))
}

func (runner *Runner) DeletePermissionsTemplate(edgeSystem string, templateName string) error {
	return runner.deleteWithMessage(edgePath("edge-systems", edgeSystem, "permissions-templates", templateName),
		fmt.Sprintf("Permissions template '%s' deleted from Provisioning Service '%s'.", templateName, edgeSystem))
}

// ── Domain Templates ──────────────────────────────────────────────────────────

func (runner *Runner) CreateDomainTemplate(edgeSystem string, domainID int, governanceTemplate string, domainTag string, customGovernanceFile string, customGovernanceName string) error {
	payload := map[string]any{"domainId": domainID, "governanceTemplate": governanceTemplate}
	if domainTag != "" {
		payload["domainTag"] = domainTag
	}
	if customGovernanceFile != "" {
		data, err := runner.ReadFile(customGovernanceFile)
		if err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Error reading custom governance XML file: %v\n", err)
			return nil
		}
		payload["customGovernanceXml"] = string(data)
		if customGovernanceName != "" {
			payload["customGovernanceName"] = customGovernanceName
		}
	}
	return runner.postJSON(edgePath("edge-systems", edgeSystem, "domain-templates"), payload, http.StatusCreated)
}

func (runner *Runner) ListDomainTemplates(edgeSystem string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "domain-templates"))
}

func (runner *Runner) DeleteDomainTemplate(edgeSystem string, templateID string) error {
	return runner.deleteWithMessage(edgePath("edge-systems", edgeSystem, "domain-templates", templateID),
		fmt.Sprintf("Domain template '%s' deleted from Provisioning Service '%s'.", templateID, edgeSystem))
}

// ── Participant Templates ─────────────────────────────────────────────────────

func (runner *Runner) CreateParticipantTemplate(edgeSystem string, name string, permissionsRef string, artifactMaxTTLMinutes int) error {
	payload := map[string]any{
		"name":           name,
		"permissionsRef": permissionsRef,
	}
	if artifactMaxTTLMinutes > 0 {
		payload["artifactMaxTtlMinutes"] = artifactMaxTTLMinutes
	}
	return runner.postJSON(edgePath("edge-systems", edgeSystem, "participant-templates"), payload, http.StatusCreated)
}

func (runner *Runner) ListParticipantTemplates(edgeSystem string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "participant-templates"))
}

func (runner *Runner) GetParticipantTemplate(edgeSystem string, templateName string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "participant-templates", templateName))
}

func (runner *Runner) DeleteParticipantTemplate(edgeSystem string, templateName string) error {
	return runner.deleteWithMessage(edgePath("edge-systems", edgeSystem, "participant-templates", templateName),
		fmt.Sprintf("Participant template '%s' deleted from Provisioning Service '%s'.", templateName, edgeSystem))
}

// ── Catalogue fetchers ────────────────────────────────────────────────────────
// Data-returning variants of the List* commands above.  The List* commands
// pretty-print the raw response to Out; these decode it so interactive flows
// (e.g. the edge-sync agent first-run wizard) can present pick-lists.  Non-200
// responses become errors instead of printed diagnostics.

// fetchJSONMap performs a GET and decodes the JSON object response.
func (runner *Runner) fetchJSONMap(path string) (map[string]any, error) {
	response, err := runner.API.Get(path)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", httputil.FormatError(response.StatusCode, body))
	}
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// itemIDs extracts an identifier from each element of the first list found
// under one of listKeys, trying idKeys in order per item.  When none of the
// expected envelope keys match, it falls back to the first array-of-objects
// value in the payload so a renamed envelope key degrades gracefully instead
// of reporting an empty catalogue.
func itemIDs(payload map[string]any, listKeys []string, idKeys ...string) []string {
	for _, listKey := range listKeys {
		if items, ok := payload[listKey].([]any); ok {
			return idsFromItems(items, idKeys)
		}
	}
	for _, value := range payload {
		items, ok := value.([]any)
		if !ok || len(items) == 0 {
			continue
		}
		if _, isObject := items[0].(map[string]any); isObject {
			return idsFromItems(items, idKeys)
		}
	}
	return nil
}

func idsFromItems(items []any, idKeys []string) []string {
	ids := make([]string, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for _, idKey := range idKeys {
			if v, _ := item[idKey].(string); v != "" {
				ids = append(ids, v)
				break
			}
		}
	}
	return ids
}

// FetchEdgeSystems returns the identifiers of all Provisioning Services on the
// account, sorted.
func (runner *Runner) FetchEdgeSystems() ([]string, error) {
	payload, err := runner.fetchJSONMap(edgePath("edge-systems"))
	if err != nil {
		return nil, err
	}
	if systems, ok := payload["edgeSystems"].(map[string]any); ok {
		names := make([]string, 0, len(systems))
		for name := range systems {
			names = append(names, name)
		}
		sort.Strings(names)
		return names, nil
	}
	return itemIDs(payload, []string{"edgeSystems", "edge_systems"}, "edge_system_id", "id", "name"), nil
}

// FetchDomainTemplates returns the Domain Template IDs of a Provisioning
// Service (the templateId field, e.g. "1:my-domain").
func (runner *Runner) FetchDomainTemplates(edgeSystem string) ([]string, error) {
	payload, err := runner.fetchJSONMap(edgePath("edge-systems", edgeSystem, "domain-templates"))
	if err != nil {
		return nil, err
	}
	return itemIDs(payload, []string{"domain_templates", "domainTemplates", "templates"},
		"templateId", "template_id", "id"), nil
}

// FetchParticipantTemplates returns the Participant Template IDs of a
// Provisioning Service (the participant_id field).
func (runner *Runner) FetchParticipantTemplates(edgeSystem string) ([]string, error) {
	payload, err := runner.fetchJSONMap(edgePath("edge-systems", edgeSystem, "participant-templates"))
	if err != nil {
		return nil, err
	}
	return itemIDs(payload, []string{"participants", "participant_templates", "participantTemplates", "templates"},
		"participant_id", "participantId", "name"), nil
}

// ── Campaigns ───────────────────────────────────────────────────────────

func parseDevicesFromCSV(data []byte) ([]any, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	devices := make([]any, 0, len(records))
	for _, record := range records {
		if len(record) < 2 {
			return nil, fmt.Errorf("invalid CSV row: expected at least 2 fields (serial, macs), got %d", len(record))
		}
		device := map[string]any{
			"serial": record[0],
			"macs":   strings.Split(record[1], ","),
		}
		if len(record) >= 3 && record[2] != "" {
			device["name"] = record[2]
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func (runner *Runner) CreateCampaign(edgeSystem string, participantID string, enrollmentList string, domainTemplateID string) error {
	data, err := runner.ReadFile(enrollmentList)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading enrollment list: %v\n", err)
		return nil
	}
	var devices []any
	if strings.HasSuffix(strings.ToLower(enrollmentList), ".csv") {
		devices, err = parseDevicesFromCSV(data)
		if err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Error: Invalid CSV in file '%s': %v\n", enrollmentList, err)
			return nil
		}
	} else {
		if err := json.Unmarshal(data, &devices); err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Error: Invalid JSON in file '%s': %v\n", enrollmentList, err)
			return nil
		}
	}
	payload := map[string]any{"devices": devices, "domainTemplateId": domainTemplateID, "participantTemplateId": participantID}
	return runner.postJSON(edgePath("edge-systems", edgeSystem, "campaigns"), payload, http.StatusCreated)
}

func (runner *Runner) ListCampaigns(edgeSystem string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "campaigns"))
}

func (runner *Runner) ListCampaignDevices(edgeSystem string, campaignID string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "campaigns", campaignID, "devices"))
}

func (runner *Runner) DeleteCampaign(edgeSystem string, campaignID string) error {
	return runner.deleteWithMessage(edgePath("edge-systems", edgeSystem, "campaigns", campaignID),
		fmt.Sprintf("Campaign '%s' deleted successfully.", campaignID))
}

// ── Devices ─────────────────────────────────────────────────────────────

func (runner *Runner) ListEdgeDevices(edgeSystem string) error {
	return runner.getJSON(edgePath("edge-systems", edgeSystem, "devices"))
}

func (runner *Runner) RevokeDevice(edgeSystem string, participantID string, campaignID string, serial string) error {
	return runner.deleteWithMessage(
		edgePath("edge-systems", edgeSystem, "participants", participantID, "campaigns", campaignID, "devices", serial),
		fmt.Sprintf("Device '%s' revoked successfully.", serial))
}

// EnrollDevice enrolls a device with the Provisioning Service and persists
// the resulting security artifacts.  It returns the domain_template_id from
// the enrollment response so callers can route subsequent operations to the
// correct store slot (<domain_template_id>/<participant_template_id>/).
//
// When an EdgeStore is configured (the production path) the artifacts are
// written directly to the device slot; otherwise the raw JSON response is
// printed to runner.Out.
func (runner *Runner) EnrollDevice(edgeSystemID string, participantID string, serial string, macs []string, csrFile string, keyFile string, campaignToken string) (string, error) {
	data, err := runner.ReadFile(csrFile)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading CSR file: %v\n", err)
		return "", fmt.Errorf("reading CSR file: %w", err)
	}
	payload := map[string]any{
		"serial": serial,
		"macs":   macs,
		"csr":    string(data),
	}
	path := edgePath("edge-systems", edgeSystemID, "enroll")
	var response *http.Response
	if campaignToken != "" {
		response, err = runner.API.PostWithBearerToken(path, payload, campaignToken)
	} else {
		response, err = runner.API.Post(path, payload)
	}
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return "", fmt.Errorf("HTTP %d: enrollment rejected", response.StatusCode)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	// Extract the domain_template_id returned by the server.  This becomes
	// the top-level directory for all on-disk artifacts for this profile:
	//   .connext/<domain_template_id>/<participant_template_id>/
	domainTemplateID := stringField(result, "domain_template_id")

	// Write to the local artifact store when available.
	if runner.EdgeStore != nil && edgeSystemID != "" && participantID != "" {
		// Layered layout identifiers: the provisioning service, the domain
		// template, the participant template and the node (device serial).
		// Every enrolled node is fully qualified by all four; a missing
		// domain_template_id means the enrollment response is incomplete and
		// we cannot determine the artifact store location.
		if domainTemplateID == "" {
			return "", fmt.Errorf("enrollment response missing domain_template_id; cannot place artifacts")
		}
		service := edgeSystemID
		domain := domainTemplateID
		node := serial
		arts := edgestore.EnrollArtifacts{
			DeviceCertPEM: []byte(stringField(result, "certificate")),
			CAChainPEM:    []byte(stringField(result, "caChain")),
			GovernanceP7S: []byte(stringField(result, "governanceP7s")),
		}
		if keyFile != "" {
			keyData, err := runner.ReadFile(keyFile)
			if err != nil {
				_, _ = fmt.Fprintf(runner.Out, "Warning: could not read key file %s: %v\n", keyFile, err)
			} else {
				arts.PrivateKeyPEM = keyData
			}
		}
		if err := runner.EdgeStore.WriteEnrollArtifacts(service, domain, participantID, node, arts); err != nil {
			return domainTemplateID, err
		}
		// enroll.lease.json — written to the node directory when the response
		// contains a top-level "lease" or "server_time_utc" key.
		if leaseData := enrollExtractLease(result); len(leaseData) > 0 {
			leaseJSON, _ := json.MarshalIndent(leaseData, "", "  ")
			nodeDir := runner.EdgeStore.NodeDir(service, domain, participantID, node)
			leaseDest := filepath.Join(nodeDir, "enroll.lease.json")
			if err := runner.writeOutputFile(leaseDest, append(leaseJSON, '\n'), false); err != nil {
				_, _ = fmt.Fprintf(runner.Out, "Warning: could not save enrollment lease: %v\n", err)
			}
		}
		_, _ = fmt.Fprintf(runner.Out, "\nEnrolled successfully.\n  Service:          %s\n  Domain Template:  %s\n  Participant:      %s\n  Store:            %s\n",
			edgeSystemID, domain, participantID, runner.EdgeStore.NodeAgentDir(service, domain, participantID, node))
		return domainTemplateID, nil
	}

	// No local store configured (unit tests / dry run): print the raw response.
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return domainTemplateID, nil
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// EnrollDeviceDirect performs operator-initiated direct enrollment: creates the
// node inventory row and signs its certificate in one API call, authenticated
// with a normal management token (no campaign JWT required).
//
// The domainTemplateID and participantTemplateID must already exist on the
// Provisioning Service identified by edgeSystemID.  serial is the unique
// identifier chosen by the operator for this participant.  macs and deviceName
// are optional.
//
// Returns the domain_template_id confirmed by the server and the device
// endpoint URL (nodeUrl) from the enrollment response, so callers such as the
// edge-sync agent can route subsequent mTLS calls without a campaign token.
func (runner *Runner) EnrollDeviceDirect(edgeSystemID, domainTemplateID, participantTemplateID, serial string, macs []string, deviceName, csrFile, keyFile string, genKey bool) (string, string, error) {
	var csrPEM string
	var generatedKey []byte
	if genKey {
		if runner.CSRGenerator == nil {
			return "", "", fmt.Errorf("CSR generator is not configured")
		}
		key, csr, err := runner.CSRGenerator(edgeSystemID, participantTemplateID, serial)
		if err != nil {
			return "", "", fmt.Errorf("generating private key and CSR: %w", err)
		}
		generatedKey = key
		csrPEM = csr
		_, _ = fmt.Fprintln(runner.Out, "Generated private key and CSR.")
	} else {
		data, err := runner.ReadFile(csrFile)
		if err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Error reading CSR file: %v\n", err)
			return "", "", fmt.Errorf("reading CSR file: %w", err)
		}
		csrPEM = string(data)
	}
	payload := map[string]any{
		"serial":                serial,
		"csr":                   csrPEM,
		"domainTemplateId":      domainTemplateID,
		"participantTemplateId": participantTemplateID,
	}
	if len(macs) > 0 {
		payload["macs"] = macs
	}
	if deviceName != "" {
		payload["name"] = deviceName
	}
	path := edgePath("edge-systems", edgeSystemID, "enroll-node")
	response, err := runner.API.Post(path, payload)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return "", "", fmt.Errorf("HTTP %d: direct enrollment rejected", response.StatusCode)
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return "", "", err
	}

	// Written only now that the enrollment is accepted: --key-file may hold the
	// key for the operator's current certificate. Kept ahead of the store block,
	// which is skipped when no store is configured and would lose the only copy.
	if generatedKey != nil && keyFile != "" {
		if err := runner.writeOutputFile(keyFile, generatedKey, true); err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Warning: could not write key file %s: %v\n", keyFile, err)
		}
	}

	// The server confirms (or overrides) the domain_template_id; fall back to
	// the caller-supplied value if the response omits it.
	retDomainTemplateID := stringField(result, "domain_template_id")
	nodeURL := stringField(result, "nodeUrl")
	if retDomainTemplateID == "" {
		retDomainTemplateID = domainTemplateID
	}

	// Write to the local artifact store when available.
	if runner.EdgeStore != nil && edgeSystemID != "" && participantTemplateID != "" {
		service := edgeSystemID
		domain := retDomainTemplateID
		node := serial
		arts := edgestore.EnrollArtifacts{
			DeviceCertPEM: []byte(stringField(result, "certificate")),
			CAChainPEM:    []byte(stringField(result, "caChain")),
			GovernanceP7S: []byte(stringField(result, "governanceP7s")),
		}
		if generatedKey != nil {
			arts.PrivateKeyPEM = generatedKey
		} else if keyFile != "" {
			keyData, err := runner.ReadFile(keyFile)
			if err != nil {
				_, _ = fmt.Fprintf(runner.Out, "Warning: could not read key file %s: %v\n", keyFile, err)
			} else {
				arts.PrivateKeyPEM = keyData
			}
		}
		if err := runner.EdgeStore.WriteEnrollArtifacts(service, domain, participantTemplateID, node, arts); err != nil {
			return retDomainTemplateID, nodeURL, err
		}
		// Persist the device endpoint URL into the node slot so subsequent
		// commands (e.g. edge-sync identity) resolve it from the correct
		// folder without requiring --url. An empty nodeURL is silently
		// ignored by WriteNodeURL.
		if err := runner.EdgeStore.WriteNodeURL(service, domain, participantTemplateID, node, nodeURL); err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Warning: could not save device URL: %v\n", err)
		}
		if leaseData := enrollExtractLease(result); len(leaseData) > 0 {
			leaseJSON, _ := json.MarshalIndent(leaseData, "", "  ")
			nodeDir := runner.EdgeStore.NodeDir(service, domain, participantTemplateID, node)
			leaseDest := filepath.Join(nodeDir, "enroll.lease.json")
			if err := runner.writeOutputFile(leaseDest, append(leaseJSON, '\n'), false); err != nil {
				_, _ = fmt.Fprintf(runner.Out, "Warning: could not save enrollment lease: %v\n", err)
			}
		}
		_, _ = fmt.Fprintf(runner.Out, "\nEnrolled successfully.\n  Service:          %s\n  Domain Template:  %s\n  Participant:      %s\n  Store:            %s\n",
			edgeSystemID, domain, participantTemplateID, runner.EdgeStore.NodeAgentDir(service, domain, participantTemplateID, node))
		return retDomainTemplateID, nodeURL, nil
	}

	// No local store configured (unit tests / dry run): print the raw response.
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return retDomainTemplateID, nodeURL, nil
}

// enrollExtractLease picks "lease" and "server_time_utc" from an enrollment
// response, returning nil when neither key is present.
func enrollExtractLease(result map[string]any) map[string]any {
	out := map[string]any{}
	if v, ok := result["lease"]; ok {
		out["lease"] = v
	}
	if v, ok := result["serverTimeUtc"]; ok {
		out["serverTimeUtc"] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
