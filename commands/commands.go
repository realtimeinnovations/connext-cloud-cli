package commands

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

type API interface {
	Get(path string) (*http.Response, error)
	Post(path string, payload any) (*http.Response, error)
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
	runner.Sleep(5 * time.Second)
	status, exists, err := runner.queryDatabusStatus(name)
	if err != nil || !exists {
		return status, exists, err
	}
	for status == previousStatus {
		runner.Sleep(5 * time.Second)
		status, exists, err = runner.queryDatabusStatus(name)
		if err != nil || !exists {
			return status, exists, err
		}
	}
	return status, exists, nil
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
