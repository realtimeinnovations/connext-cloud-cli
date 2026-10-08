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
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/clierror"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/httputil"
)

type resultEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	Data          any    `json:"data"`
}

func (runner *Runner) writeResult(data any) error {
	if runner.JSON {
		return json.NewEncoder(runner.Out).Encode(resultEnvelope{SchemaVersion: "1", Data: data})
	}
	encoder := json.NewEncoder(runner.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

// mutationResult keeps human-readable confirmations in text mode and emits
// exactly one structured result in JSON mode.
func (runner *Runner) mutationResult(data any, message string) error {
	if runner.JSON {
		return runner.writeResult(data)
	}
	_, err := fmt.Fprintln(runner.Out, message)
	return err
}

func invalidInput(message string, cause error) error {
	return &clierror.Error{Code: clierror.CodeInvalidArgument, Message: message, Cause: cause}
}

func readResponse(response *http.Response, allowed ...int) ([]byte, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	for _, status := range allowed {
		if response.StatusCode == status {
			return body, nil
		}
	}
	return nil, httputil.NewStatusError(response.StatusCode, body)
}

func (runner *Runner) progress(message string) error {
	if runner.JSON {
		return nil
	}
	out := runner.Out
	_, err := fmt.Fprintln(out, message)
	return err
}

func decodeDatabus(response *http.Response) (map[string]any, error) {
	body, err := readResponse(response, http.StatusOK)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned invalid databus JSON: " + err.Error(), Cause: err}
	}
	if payload == nil {
		return nil, &clierror.Error{Code: clierror.CodeInvalidResponse, Message: "API returned null instead of a databus object"}
	}
	return payload, nil
}

func (runner *Runner) fetchDatabus(name string) (map[string]any, bool, error) {
	response, err := runner.API.Get("/databuses/" + url.PathEscape(name))
	if err != nil {
		return nil, false, err
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return nil, false, nil
	}
	payload, err := decodeDatabus(response)
	return payload, true, err
}

func (runner *Runner) ListDatabuses(short bool) error {
	if runner.JSON && short {
		return &clierror.Error{Code: clierror.CodeInvalidArgument, Message: "--short cannot be combined with --format json"}
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
	if !short {
		return runner.writeResult(payload)
	}
	names := make([]string, 0, len(resources))
	for name := range resources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		kind := "databus"
		if info, ok := resources[name].(map[string]any); ok {
			if value, ok := info["kind"].(string); ok && value != "" {
				kind = value
			}
		}
		if _, err := fmt.Fprintf(runner.Out, "- %s (%s)\n", name, kind); err != nil {
			return err
		}
	}
	return nil
}

func (runner *Runner) QueryDatabus(name string) error {
	response, err := runner.API.Get("/databuses/" + url.PathEscape(name))
	if err != nil {
		return err
	}
	payload, err := decodeDatabus(response)
	if err != nil {
		return err
	}
	return runner.writeResult(payload)
}

// waitForDatabusTerminal confirms the desired outcome, rather than treating
// every state change or non-200 response as successful completion.
func (runner *Runner) waitForDatabusTerminal(name string, deleting bool) (map[string]any, error) {
	return runner.waitForResourceTerminal(name, "databus", deleting)
}

func (runner *Runner) waitForResourceTerminal(name, kind string, deleting bool) (map[string]any, error) {
	for waited := time.Duration(0); ; waited += databusStatusPollInterval {
		payload, exists, err := runner.fetchDatabus(name)
		if err != nil {
			return nil, err
		}
		if !exists {
			if deleting {
				return map[string]any{"name": name, "status": "deleted"}, nil
			}
			return nil, &clierror.Error{Code: clierror.CodeOperationFailed, Message: fmt.Sprintf("%s %q disappeared before creation completed", kind, name)}
		}
		status, _ := payload["status"].(string)
		if !deleting && status == "active" {
			if _, ok := payload["name"]; !ok {
				payload["name"] = name
			}
			return payload, nil
		}
		pending := "creating"
		if deleting {
			pending = "deleting"
		}
		if status != pending {
			return nil, &clierror.Error{Code: clierror.CodeOperationFailed, Message: fmt.Sprintf("%s %q left %q with unexpected status %q", kind, name, pending, status)}
		}
		if waited >= databusStatusWaitTimeout {
			return nil, &clierror.Error{Code: clierror.CodeTimeout, Message: fmt.Sprintf("timed out waiting for %s %q to leave %q after %s", kind, name, pending, databusStatusWaitTimeout)}
		}
		runner.Sleep(databusStatusPollInterval)
	}
}

func checkDatabusMutation(response *http.Response, allowed ...int) error {
	_, err := readResponse(response, allowed...)
	return err
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
	if err := checkDatabusMutation(response, http.StatusCreated); err != nil {
		return err
	}
	if err := runner.progress("Databus creation started successfully.\nWaiting for creation to complete... (safe to Ctrl+C)"); err != nil {
		return err
	}
	resource, err := runner.waitForDatabusTerminal(name, false)
	if err != nil {
		return err
	}
	if runner.JSON {
		return runner.writeResult(resource)
	}
	return runner.progress("Databus status:  active")
}

func (runner *Runner) DeleteDatabus(name string) error {
	response, err := runner.API.Delete("/databuses/" + url.PathEscape(name))
	if err != nil {
		return err
	}
	if err := checkDatabusMutation(response, http.StatusOK, http.StatusNoContent); err != nil {
		return err
	}
	if err := runner.progress("Databus deletion started successfully.\nWaiting for databus deletion to complete... (safe to Ctrl+C)"); err != nil {
		return err
	}
	resource, err := runner.waitForDatabusTerminal(name, true)
	if err != nil {
		return err
	}
	if runner.JSON {
		return runner.writeResult(resource)
	}
	return runner.progress("Databus has been deleted")
}
