package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

func (runner *Runner) CreateDatabus(name string, replicas int, observabilityServiceName string, systemDesigner bool, networkName string) error {
	payload := map[string]any{"name": name, "replicas": replicas, "system_designer": systemDesigner, "network_name": networkName}
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

func (runner *Runner) CreateObsService(name string, networkName string) error {
	payload := map[string]any{"name": name, "replicas": 0, "enable_edge_observability": true}
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

func (runner *Runner) CreateClientConfig(name string, port int, kind string, clientName string) error {
	if kind == "observability-collector" {
		kind = "telemetry-service-collector"
	}
	payload := map[string]any{"port": port, "kind": kind}
	if clientName != "" {
		payload["client_name"] = clientName
	}
	response, err := runner.API.Post("/databuses/"+name+"/applications", payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusCreated {
		_, _ = fmt.Fprintln(runner.Out, string(body))
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

func (runner *Runner) GetClientConfig(name string, clientName string, generateExample bool, forceOverwrite bool, targetDir string) error {
	response, err := runner.API.Get("/databuses/" + name + "/applications/" + clientName)
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
	clientConfig, _ := payload["client_config"].(string)
	if clientConfig == "" {
		_, _ = fmt.Fprintf(runner.Out, "Error: Unexpected client configuration for '%s'\n", clientName)
		return nil
	}
	if _, err := fmt.Fprintln(runner.Out, payload["client_data"]); err != nil {
		return err
	}
	if _, err := runner.SaveClientFile(targetDir, clientName+".xml", []byte(clientConfig), forceOverwrite); err != nil {
		return err
	}
	if generateExample {
		if example, ok := payload["client_example"].(string); ok && example != "" {
			_, err := runner.SaveClientFile(targetDir, clientName+".py", []byte(example), forceOverwrite)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (runner *Runner) DeleteClientConfig(name string, clientName string) error {
	response, err := runner.API.Delete("/databuses/" + name + "/applications/" + clientName)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Client '%s' successfully deleted from databus '%s'\n", clientName, name)
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
		if err := runner.MkdirAll(targetDir, 0o755); err != nil {
			return false, err
		}
		filePath = filepath.Join(targetDir, fileName)
	}
	if _, err := runner.Stat(filePath); err == nil && !forceOverwrite {
		_, _ = fmt.Fprintf(runner.Out, "%s already exists. Use -f to overwrite.\n", filePath)
		return false, nil
	}
	if err := runner.WriteFile(filePath, data, clientFileMode(fileName)); err != nil {
		return false, err
	}
	_, _ = fmt.Fprintf(runner.Out, "Saved %s\n", filePath)
	return true, nil
}

func clientFileMode(fileName string) os.FileMode {
	if strings.HasSuffix(fileName, ".key") {
		return 0o600
	}
	return 0o644
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
	if err := runner.GetClientConfig(name, appName, true, forceOverwrite, targetDir); err != nil {
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
	payload := map[string]any{}
	if expirationDays != nil {
		if *expirationDays < 0 {
			_, _ = fmt.Fprintln(runner.Out, "Error: expiration-days must be greater than or equal to 0")
			return nil
		}
		payload["expiration_days"] = *expirationDays
	}
	response, err := runner.API.Post("/licenses", payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	if output == "" {
		_, _ = fmt.Fprintln(runner.Out, string(body))
		return nil
	}
	dir := filepath.Dir(output)
	if dir != "." {
		if err := runner.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := runner.WriteFile(output, body, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(runner.Out, "License saved to %s\n", output)
	return nil
}

// ── Provisioning Service Management ───────────────────────────────────────────────────

func (runner *Runner) ListEdgeSystems() error {
	response, err := runner.API.Get("/edge-systems")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) CreateEdgeSystem(name string, governanceFile string, description string) error {
	data, err := runner.ReadFile(governanceFile)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading governance file: %v\n", err)
		return nil
	}
	payload := map[string]any{"name": name, "governanceXml": string(data)}
	if description != "" {
		payload["description"] = description
	}
	response, err := runner.API.Post("/edge-systems", payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusAccepted {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) QueryEdgeSystem(name string) error {
	response, err := runner.API.Get("/edge-systems/" + name)
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

func (runner *Runner) DeleteEdgeSystem(name string) error {
	response, err := runner.API.Delete("/edge-systems/" + name)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Provisioning Service '%s' deleted successfully.\n", name)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

// ── Participant Profiles ────────────────────────────────────────────────────────

func (runner *Runner) CreateParticipant(edgeSystem string, name string, permissionsFile string, effectiveRevocationSeconds int) error {
	data, err := runner.ReadFile(permissionsFile)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading permissions file: %v\n", err)
		return nil
	}
	payload := map[string]any{
		"name":                       name,
		"permissionsXml":             string(data),
		"effectiveRevocationSeconds": effectiveRevocationSeconds,
	}
	response, err := runner.API.Post("/edge-systems/"+edgeSystem+"/participants", payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) ListParticipants(edgeSystem string) error {
	response, err := runner.API.Get("/edge-systems/" + edgeSystem + "/participants")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) QueryParticipant(edgeSystem string, participantID string) error {
	response, err := runner.API.Get("/edge-systems/" + edgeSystem + "/participants/" + participantID)
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

func (runner *Runner) DeleteParticipant(edgeSystem string, participantID string) error {
	response, err := runner.API.Delete("/edge-systems/" + edgeSystem + "/participants/" + participantID)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Participant '%s' deleted from Provisioning Service '%s'.\n", participantID, edgeSystem)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
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

func (runner *Runner) CreateCampaign(edgeSystem string, participantID string, devicesFile string) error {
	data, err := runner.ReadFile(devicesFile)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading devices file: %v\n", err)
		return nil
	}
	var devices []any
	if strings.HasSuffix(strings.ToLower(devicesFile), ".csv") {
		devices, err = parseDevicesFromCSV(data)
		if err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Error: Invalid CSV in file '%s': %v\n", devicesFile, err)
			return nil
		}
	} else {
		if err := json.Unmarshal(data, &devices); err != nil {
			_, _ = fmt.Fprintf(runner.Out, "Error: Invalid JSON in file '%s': %v\n", devicesFile, err)
			return nil
		}
	}
	payload := map[string]any{"devices": devices}
	response, err := runner.API.Post("/edge-systems/"+edgeSystem+"/participants/"+participantID+"/campaigns", payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusCreated {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var result any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(result, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) ListCampaigns(edgeSystem string, participantID string) error {
	response, err := runner.API.Get("/edge-systems/" + edgeSystem + "/participants/" + participantID + "/campaigns")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) ListCampaignDevices(edgeSystem string, participantID string, campaignID string) error {
	response, err := runner.API.Get("/edge-systems/" + edgeSystem + "/participants/" + participantID + "/campaigns/" + campaignID + "/devices")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) DeleteCampaign(edgeSystem string, participantID string, campaignID string) error {
	response, err := runner.API.Delete("/edge-systems/" + edgeSystem + "/participants/" + participantID + "/campaigns/" + campaignID)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Campaign '%s' deleted successfully.\n", campaignID)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

// ── Devices ─────────────────────────────────────────────────────────────

func (runner *Runner) ListEdgeDevices(edgeSystem string) error {
	response, err := runner.API.Get("/edge-systems/" + edgeSystem + "/devices")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	formatted, _ := json.MarshalIndent(payload, "", "  ")
	_, _ = fmt.Fprintln(runner.Out, string(formatted))
	return nil
}

func (runner *Runner) RevokeDevice(edgeSystem string, participantID string, campaignID string, serial string) error {
	response, err := runner.API.Delete("/edge-systems/" + edgeSystem + "/participants/" + participantID + "/campaigns/" + campaignID + "/devices/" + serial)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode == http.StatusOK {
		_, _ = fmt.Fprintf(runner.Out, "Device '%s' revoked successfully.\n", serial)
		return nil
	}
	runner.printResponseError("Error: ", response.StatusCode, body)
	return nil
}

// enrollArtifacts maps enrollment response fields to their output file names.
var enrollArtifacts = []struct{ field, filename string }{
	{"certificate", "identity.crt"},
	{"ca_chain", "identity-ca-chain.crt"},
	{"governance_p7s", "signed_governance.p7s"},
}

func (runner *Runner) EnrollDevice(edgeSystemID string, participantID string, serial string, macs []string, csrFile string, keyFile string, campaignToken string, outputDir string) error {
	data, err := runner.ReadFile(csrFile)
	if err != nil {
		_, _ = fmt.Fprintf(runner.Out, "Error reading CSR file: %v\n", err)
		return nil
	}
	payload := map[string]any{
		"serial": serial,
		"macs":   macs,
		"csr":    string(data),
	}
	path := "/edge-systems/" + edgeSystemID + "/participants/" + participantID + "/enroll"
	var response *http.Response
	if campaignToken != "" {
		response, err = runner.API.PostWithBearerToken(path, payload, campaignToken)
	} else {
		response, err = runner.API.Post(path, payload)
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}

	// Write to the local artifact store when available.
	if runner.EdgeStore != nil && edgeSystemID != "" && participantID != "" {
		arts := edgestore.EnrollArtifacts{
			DeviceCertPEM: []byte(stringField(result, "certificate")),
			CAChainPEM:    []byte(stringField(result, "ca_chain")),
			GovernanceP7S: []byte(stringField(result, "governance_p7s")),
		}
		if keyFile != "" {
			keyData, err := runner.ReadFile(keyFile)
			if err != nil {
				_, _ = fmt.Fprintf(runner.Out, "Warning: could not read key file %s: %v\n", keyFile, err)
			} else {
				arts.PrivateKeyPEM = keyData
			}
		}
		if err := runner.EdgeStore.WriteArtifacts(edgeSystemID, participantID, arts); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(runner.Out, "\nEnrolled successfully.\n  Service:     %s\n  Participant: %s\n  Store:       %s\n",
			edgeSystemID, participantID, runner.EdgeStore.SlotDir(edgeSystemID, participantID))
		return nil
	}

	if outputDir == "" {
		formatted, _ := json.MarshalIndent(result, "", "  ")
		_, _ = fmt.Fprintln(runner.Out, string(formatted))
		return nil
	}
	if err := runner.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	for _, a := range enrollArtifacts {
		val, ok := result[a.field].(string)
		if !ok || val == "" {
			continue
		}
		destPath := filepath.Join(outputDir, a.filename)
		if err := runner.WriteFile(destPath, []byte(val), 0o644); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(runner.Out, "Saved %s\n", destPath)
	}
	return nil
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
