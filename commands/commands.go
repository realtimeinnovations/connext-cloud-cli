// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package commands

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
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
	JSON         bool
	Sleep        func(time.Duration)
	ReadFile     func(string) ([]byte, error)
	WriteFile    func(string, []byte, os.FileMode) error
	Chmod        func(string, os.FileMode) error
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
		Chmod:     os.Chmod,
		MkdirAll:  os.MkdirAll,
		Stat:      os.Stat,
	}
}

func (runner *Runner) printResponseError(prefix string, statusCode int, body []byte) {
	_, _ = fmt.Fprintf(runner.Out, "%s%s\n", prefix, httputil.FormatError(statusCode, body))
}

func (runner *Runner) printResponseBody(body []byte) error {
	var payload any
	if json.Unmarshal(body, &payload) == nil {
		body, _ = json.MarshalIndent(payload, "", "  ")
	}
	_, err := fmt.Fprintln(runner.Out, string(body))
	return err
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
	if sensitive {
		if err := runner.Chmod(filePath, fileMode); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := runner.WriteFile(filePath, data, fileMode); err != nil {
		return err
	}
	if sensitive {
		return runner.Chmod(filePath, fileMode)
	}
	return nil
}

func (runner *Runner) ListTopics(name string) error {
	response, err := runner.API.Get("/databuses/" + name + "/topics")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		runner.printResponseError("Error: ", response.StatusCode, body)
		return nil
	}
	runner.printResponseBody(body)
	return nil
}

func (runner *Runner) GetTopic(name string, topicName string, typeXML bool) error {
	path := "/databuses/" + name + "/topics/" + url.PathEscape(topicName)
	if typeXML {
		path += "?representation=typeXml"
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
	if typeXML {
		_, err = runner.Out.Write(body)
		return err
	}
	runner.printResponseBody(body)
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
	if err := checkDatabusMutation(response, http.StatusCreated); err != nil {
		return err
	}
	if err := runner.progress("Observability Service creation started successfully.\nWaiting for creation to complete... (safe to Ctrl+C)"); err != nil {
		return err
	}
	resource, err := runner.waitForResourceTerminal(name, "Observability Service", false)
	if err != nil {
		return err
	}
	return runner.mutationResult(resource, "Observability Service status:  active")
}

func (runner *Runner) ListObservabilityServices(short bool) error {
	if runner.JSON && short {
		return invalidInput("--short cannot be combined with --format json", nil)
	}
	path := "/databuses"
	if !short {
		path += "?extra_fields=true"
	}
	response, err := runner.API.Get(path)
	if err != nil {
		return err
	}
	payload, err := decodeDatabus(response)
	if err != nil {
		return err
	}
	resources, ok := payload["databuses"].(map[string]any)
	if !ok {
		return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API response is missing the databuses object"}
	}
	observability := map[string]any{}
	for name, rawInfo := range resources {
		info, _ := rawInfo.(map[string]any)
		if kind, _ := info["kind"].(string); kind == "telemetry" {
			observability[name] = info
		}
	}
	if !short {
		return runner.writeResult(map[string]any{"observability_services": observability})
	}
	names := make([]string, 0, len(observability))
	for name := range observability {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintf(runner.Out, "- %s\n", name); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) QueryObservabilityService(name string) error {
	return runner.QueryDatabus(name)
}

func (runner *Runner) DeleteObservabilityService(name string) error {
	response, err := runner.API.Delete("/databuses/" + url.PathEscape(name))
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	if err := runner.progress("Observability Service deletion started successfully.\nWaiting for deletion to complete... (safe to Ctrl+C)"); err != nil {
		return err
	}
	resource, err := runner.waitForResourceTerminal(name, "Observability Service", true)
	if err != nil {
		return err
	}
	return runner.mutationResult(resource, "Observability Service has been deleted")
}

func (runner *Runner) UpdateObservabilityLink(name string, observabilityServiceName any) error {
	response, err := runner.API.Patch("/databuses/"+url.PathEscape(name), map[string]any{"observability_service_name": observabilityServiceName})
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK); err != nil {
		return err
	}
	action := "linked"
	if observabilityServiceName == nil || observabilityServiceName == "" {
		action = "unlinked"
	}
	return runner.mutationResult(map[string]any{"name": name, "observability_service_name": observabilityServiceName, "status": action}, fmt.Sprintf("Observability Service %s for Databus '%s'", action, name))
}

func (runner *Runner) UpdateDatabusStatus(name string, status string) error {
	response, err := runner.API.Patch("/databuses/"+url.PathEscape(name), map[string]any{"running_status": status})
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"name": name, "running_status": status, "status": "updated"}, fmt.Sprintf("Databus '%s' %sd successfully.", name, status))
}

func (runner *Runner) ListNetworks() error { return runner.getJSON("/networks") }

func (runner *Runner) DeleteNetwork(name string) error {
	response, err := runner.API.Delete("/networks/" + url.PathEscape(name))
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"name": name, "status": "deleted"}, fmt.Sprintf("Network '%s' deleted successfully.", name))
}

func (runner *Runner) UpdateFilters(name string, filterFile string) error {
	data, err := runner.ReadFile(filterFile)
	if err != nil {
		return invalidInput(fmt.Sprintf("Error reading filter file: %v", err), err)
	}
	var filtersData any
	if err := json.Unmarshal(data, &filtersData); err != nil {
		return invalidInput(fmt.Sprintf("Invalid JSON in file %q: %v", filterFile, err), err)
	}
	switch filtersData.(type) {
	case map[string]any, []any:
	default:
		return invalidInput("Filter file must contain a JSON object or array", nil)
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
			if err := runner.progress("Converting JSON file."); err != nil {
				return err
			}
		}
	}
	response, err := runner.API.Patch("/databuses/"+url.PathEscape(name), map[string]any{"persistence_filters": filtersData})
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"name": name, "persistence_filters": filtersData, "status": "updated"}, fmt.Sprintf("Filters for databus '%s' updated successfully.", name))
}

func (runner *Runner) AddUserToDatabus(name string, email string) error {
	response, err := runner.API.Post("/databuses/"+url.PathEscape(name)+"/users/"+url.PathEscape(email)+"/", nil)
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusCreated); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"name": name, "email": email, "status": "added"}, fmt.Sprintf("User '%s' successfully added to databus '%s'", email, name))
}

func (runner *Runner) RemoveUserFromDatabus(name string, email string) error {
	response, err := runner.API.Delete("/databuses/" + url.PathEscape(name) + "/users/" + url.PathEscape(email) + "/")
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"name": name, "email": email, "status": "removed"}, fmt.Sprintf("User '%s' successfully removed from databus '%s'", email, name))
}

func (runner *Runner) GetLicense(expirationDays *int, output string) error {
	body, err := runner.DownloadLicense(expirationDays)
	if err != nil {
		return err
	}
	if output == "" {
		var license any
		if json.Unmarshal(body, &license) == nil && license != nil {
			return runner.writeResult(license)
		}
		return runner.mutationResult(map[string]any{"license": string(body), "status": "downloaded"}, string(body))
	}
	if err := runner.writeOutputFile(output, body, false); err != nil {
		return err
	}
	absolutePath, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"artifact_path": absolutePath, "status": "saved"}, "License saved to "+output)
}

func (runner *Runner) DownloadLicense(expirationDays *int) ([]byte, error) {
	body, statusCode, err := runner.requestLicense(expirationDays)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, httputil.NewStatusError(statusCode, body)
	}
	return body, nil
}

func (runner *Runner) requestLicense(expirationDays *int) ([]byte, int, error) {
	payload := map[string]any{}
	if expirationDays != nil {
		if *expirationDays < 0 {
			return nil, 0, invalidInput("expiration-days must be greater than or equal to 0", nil)
		}
		payload["expiration_days"] = *expirationDays
	}
	response, err := runner.API.Post("/licenses", payload)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return body, response.StatusCode, err
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
		return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned invalid JSON: " + err.Error(), Cause: err}
	}
	if payload == nil {
		return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned null instead of a result"}
	}
	return runner.writeResult(payload)
}

// getJSON performs a GET and pretty-prints the response body on HTTP 200.
// Any other status is returned as an HTTP error.
func (runner *Runner) getJSON(path string) error {
	response, err := runner.API.Get(path)
	if err != nil {
		return err
	}
	body, err := readResponse(response, http.StatusOK)
	if err != nil {
		return err
	}
	return runner.printJSON(body)
}

// postJSON performs a POST and pretty-prints the response body when the
// response status equals successStatus. Any other status is returned as an HTTP error.
func (runner *Runner) postJSON(path string, payload any, successStatus int) error {
	response, err := runner.API.Post(path, payload)
	if err != nil {
		return err
	}
	body, err := readResponse(response, successStatus)
	if err != nil {
		return err
	}
	// Preserve the backend's IDs and response. An accepted operation is not
	// claimed to be completed; callers can query it using the returned IDs.
	if runner.JSON {
		var result any
		if len(bytes.TrimSpace(body)) != 0 {
			if err := json.Unmarshal(body, &result); err != nil {
				return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned invalid JSON: " + err.Error(), Cause: err}
			}
			if result == nil {
				return &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned null instead of a result"}
			}
		}
		status := "created"
		if successStatus == http.StatusAccepted {
			status = "accepted"
		}
		data := map[string]any{"path": path, "status": status, "resource": result}
		if request, ok := payload.(map[string]any); ok {
			if name, ok := request["name"]; ok {
				data["name"] = name
			}
			if domainID, ok := request["domainId"]; ok {
				data["domain_id"] = domainID
			}
		}
		return runner.writeResult(data)
	}
	return runner.printJSON(body)
}

// deleteWithMessage accepts HTTP 200 or 204 as confirmation of deletion.
// Other statuses are returned as HTTP errors.
func (runner *Runner) deleteWithMessage(path string, okMsg string) error {
	response, err := runner.API.Delete(path)
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"path": path, "status": "deleted"}, okMsg)
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
		return invalidInput(fmt.Sprintf("Error reading governance XML: %v", err), err)
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
		return invalidInput(fmt.Sprintf("Error reading permissions XML: %v", err), err)
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

func (runner *Runner) CreateDomainTemplate(edgeSystem string, domainID int, governanceTemplate string, domainTag string, customGovernanceFile string, customGovernanceName string, securityMode string, pskTTLMinutes, deviceCertTTLMinutes int) error {
	if securityMode != "full" && securityMode != "lightweight" {
		return fmt.Errorf("security mode must be full or lightweight")
	}
	if securityMode == "lightweight" && (governanceTemplate != "" || customGovernanceFile != "") {
		return fmt.Errorf("governance is not allowed with lightweight security")
	}
	if pskTTLMinutes < 0 || deviceCertTTLMinutes < 0 {
		return fmt.Errorf("artifact lifetimes must not be negative")
	}
	payload := map[string]any{"domainId": domainID, "securityMode": securityMode}
	if governanceTemplate != "" {
		payload["governanceTemplate"] = governanceTemplate
	}
	if pskTTLMinutes > 0 {
		payload["pskTtlMinutes"] = pskTTLMinutes
	}
	if deviceCertTTLMinutes > 0 {
		payload["deviceCertTtlMinutes"] = deviceCertTTLMinutes
	}
	if domainTag != "" {
		payload["domainTag"] = domainTag
	}
	if customGovernanceFile != "" {
		data, err := runner.ReadFile(customGovernanceFile)
		if err != nil {
			return invalidInput(fmt.Sprintf("Error reading custom governance XML file: %v", err), err)
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
	items := catalogItems(payload, listKeys)
	if items == nil {
		return nil
	}
	return idsFromItems(items, idKeys)
}

func catalogItems(payload map[string]any, listKeys []string) []any {
	for _, listKey := range listKeys {
		if items, ok := payload[listKey].([]any); ok {
			return items
		}
	}
	for _, value := range payload {
		items, ok := value.([]any)
		if !ok || len(items) == 0 {
			continue
		}
		if _, isObject := items[0].(map[string]any); isObject {
			return items
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

func (runner *Runner) FetchDomainTemplateMode(edgeSystem, templateID string) (string, error) {
	payload, err := runner.fetchJSONMap(edgePath("edge-systems", edgeSystem, "domain-templates"))
	if err != nil {
		return "", err
	}
	items := catalogItems(payload, []string{"domain_templates", "domainTemplates", "templates"})
	for _, raw := range items {
		template, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ids := idsFromItems([]any{template}, []string{"templateId", "template_id", "id"})
		if len(ids) == 0 || ids[0] != templateID {
			continue
		}
		rawMode, present := template["securityMode"]
		if !present {
			// Assume full security for legacy templates that omit securityMode.
			return "full", nil
		}
		mode, ok := rawMode.(string)
		if !ok || (mode != "full" && mode != "lightweight") {
			return "", fmt.Errorf("domain template %q has invalid security mode %v", templateID, rawMode)
		}
		return mode, nil
	}
	return "", fmt.Errorf("domain template %q not found", templateID)
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
		return invalidInput(fmt.Sprintf("Error reading enrollment list: %v", err), err)
	}
	var devices []any
	if strings.HasSuffix(strings.ToLower(enrollmentList), ".csv") {
		devices, err = parseDevicesFromCSV(data)
		if err != nil {
			return invalidInput(fmt.Sprintf("Invalid CSV in file %q: %v", enrollmentList, err), err)
		}
	} else {
		if err := json.Unmarshal(data, &devices); err != nil {
			return invalidInput(fmt.Sprintf("Invalid JSON in file %q: %v", enrollmentList, err), err)
		}
	}
	if devices == nil {
		return invalidInput("Enrollment list must contain a JSON array or CSV records, not null", nil)
	}
	payload := map[string]any{"devices": devices, "domainTemplateId": domainTemplateID}
	if participantID != "" {
		payload["participantTemplateId"] = participantID
	}
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
	path := edgePath("edge-systems", edgeSystem, "participants", participantID, "campaigns", campaignID, "devices", serial)
	response, err := runner.API.Delete(path)
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	return runner.mutationResult(map[string]any{"path": path, "serial": serial, "status": "revoked"}, fmt.Sprintf("Device '%s' revoked successfully.", serial))
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
	securityMode, err := enrollmentSecurityMode(result)
	if err != nil {
		return "", err
	}

	// Extract the domain_template_id returned by the server.  This becomes
	// the top-level directory for all on-disk artifacts for this profile:
	//   .connext/<domain_template_id>/<participant_template_id>/
	domainTemplateID := stringField(result, "domain_template_id")

	// Write to the local artifact store when available.
	if runner.EdgeStore != nil && edgeSystemID != "" {
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
			SecurityMode:  securityMode,
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

func enrollmentSecurityMode(result map[string]any) (string, error) {
	rawMode, present := result["security_mode"]
	if !present {
		return "full", nil
	}
	mode, ok := rawMode.(string)
	if !ok || (mode != "full" && mode != "lightweight") {
		return "", fmt.Errorf("enrollment response has invalid security_mode %v", rawMode)
	}
	return mode, nil
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
		"serial":           serial,
		"csr":              csrPEM,
		"domainTemplateId": domainTemplateID,
	}
	if participantTemplateID != "" {
		payload["participantTemplateId"] = participantTemplateID
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
	securityMode, err := enrollmentSecurityMode(result)
	if err != nil {
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
	if runner.EdgeStore != nil && edgeSystemID != "" {
		service := edgeSystemID
		domain := retDomainTemplateID
		node := serial
		arts := edgestore.EnrollArtifacts{
			SecurityMode:  securityMode,
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
