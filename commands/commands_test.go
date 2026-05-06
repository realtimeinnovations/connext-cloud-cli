package commands

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeAPI struct {
	lastPath    string
	lastPayload any
	responses   map[string]*http.Response
}

func (api *fakeAPI) response(path string) (*http.Response, error) {
	if response, ok := api.responses[path]; ok {
		return response, nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing"))}, nil
}

func (api *fakeAPI) Get(path string) (*http.Response, error) {
	api.lastPath = path
	return api.response("GET " + path)
}

func (api *fakeAPI) Post(path string, payload any) (*http.Response, error) {
	api.lastPath = path
	api.lastPayload = payload
	return api.response("POST " + path)
}

func (api *fakeAPI) Patch(path string, payload any) (*http.Response, error) {
	api.lastPath = path
	api.lastPayload = payload
	return api.response("PATCH " + path)
}

func (api *fakeAPI) Delete(path string) (*http.Response, error) {
	api.lastPath = path
	return api.response("DELETE " + path)
}

func newJSONResponse(status int, payload any) *http.Response {
	data, _ := json.Marshal(payload)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data))}
}

func newTextResponse(status int, payload string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(payload))}
}

func TestCreateClientConfigMapsObservabilityCollectorKind(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /databuses/obs/applications": newTextResponse(http.StatusCreated, "ok")}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.CreateClientConfig("obs", 7777, "observability-collector", "collector"); err != nil {
		t.Fatal(err)
	}
	payload, ok := api.lastPayload.(map[string]any)
	if !ok || payload["kind"] != "telemetry-service-collector" {
		t.Fatalf("unexpected payload: %#v", api.lastPayload)
	}
}

func TestUpdateFiltersConvertsLegacyJSONList(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"PATCH /databuses/db": newTextResponse(http.StatusOK, "ok")}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte(`[{
			"topic_name": "Square",
			"topic_filter": "x > 1"
		}]`), nil
	}
	if err := runner.UpdateFilters("db", "filters.json"); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	persistenceFilters := payload["persistence_filters"].(map[string]any)
	contentFilters := persistenceFilters["contentFilters"].([]map[string]any)
	if len(contentFilters) != 1 || contentFilters[0]["topicName"] != "Square" || contentFilters[0]["expression"] != "x > 1" {
		t.Fatalf("unexpected filter payload: %#v", persistenceFilters)
	}
	if !strings.Contains(out.String(), "Converting JSON file.") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestListDatabusesShortPrintsKindPerResource(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"GET /databuses": newJSONResponse(http.StatusOK, map[string]any{"databuses": map[string]any{"db": map[string]any{"kind": "databus"}, "obs": map[string]any{"kind": "telemetry"}}})}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListDatabuses(true); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if !strings.Contains(output, "- db (databus)") || !strings.Contains(output, "- obs (telemetry)") {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestCreateDatabusWaitsForStatusChange(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /databuses":            newTextResponse(http.StatusCreated, "{}"),
		"GET /databuses/inventory":   newJSONResponse(http.StatusOK, map[string]any{"status": "ready"}),
		"GET /databuses/inventory-2": newJSONResponse(http.StatusOK, map[string]any{"status": "ready"}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.Sleep = func(time.Duration) {}
	if err := runner.CreateDatabus("inventory", 2, "", false, ""); err != nil {
		t.Fatal(err)
	}
	output := out.String()
	if !strings.Contains(output, "Waiting for creation to complete") || !strings.Contains(output, "Databus status:  ready") {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestUpdateObservabilityLinkSendsNullForUnlink(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"PATCH /databuses/db": newTextResponse(http.StatusOK, "{}")}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.UpdateObservabilityLink("db", nil); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if _, exists := payload["observability_service_name"]; !exists || payload["observability_service_name"] != nil {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestGetLicenseWritesOutputFile(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /licenses": newTextResponse(http.StatusOK, "license-body")}}
	var out bytes.Buffer
	runner := New(api, &out)
	target := filepath.Join(t.TempDir(), "rti_license.dat")
	days := 30
	if err := runner.GetLicense(&days, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "license-body" {
		t.Fatalf("unexpected license content: %s", data)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["expiration_days"] != days {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !strings.Contains(out.String(), "License saved to "+target) {
		t.Fatalf("unexpected output: %s", out.String())
	}
}
