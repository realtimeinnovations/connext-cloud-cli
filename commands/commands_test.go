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
	getFunc     func(string) (*http.Response, error)
}

func (api *fakeAPI) response(path string) (*http.Response, error) {
	if response, ok := api.responses[path]; ok {
		return response, nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("missing"))}, nil
}

func (api *fakeAPI) Get(path string) (*http.Response, error) {
	api.lastPath = path
	if api.getFunc != nil {
		return api.getFunc(path)
	}
	return api.response("GET " + path)
}

func (api *fakeAPI) Post(path string, payload any) (*http.Response, error) {
	api.lastPath = path
	api.lastPayload = payload
	return api.response("POST " + path)
}

func (api *fakeAPI) PostWithBearerToken(path string, payload any, _ string) (*http.Response, error) {
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

func TestCreateDatabusTimesOutWaitingForStatusChange(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /databuses": newTextResponse(http.StatusCreated, "{}")}}
	api.getFunc = func(path string) (*http.Response, error) {
		if path != "/databuses/inventory" {
			return newTextResponse(http.StatusNotFound, "missing"), nil
		}
		return newJSONResponse(http.StatusOK, map[string]any{"status": "creating"}), nil
	}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.Sleep = func(time.Duration) {}
	err := runner.CreateDatabus("inventory", 2, "", false, "")
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for databus \"inventory\" to leave \"creating\"") {
		t.Fatalf("unexpected error: %v", err)
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

// ── Edge System Tests ────────────────────────────────────────────────────────

func TestListEdgeSystems(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems": newJSONResponse(http.StatusOK, map[string]any{
			"edgeSystems": map[string]any{"alpha": map[string]any{"status": "active"}},
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListEdgeSystems(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "alpha") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestCreateEdgeSystem(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems": newJSONResponse(http.StatusAccepted, map[string]any{
			"message": "EdgeSystem 'alpha' creation started",
			"id":      "ces-alpha-123",
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		if path != "gov.xml" {
			t.Fatalf("unexpected governance file path: %s", path)
		}
		return []byte("<governance/>"), nil
	}
	if err := runner.CreateEdgeSystem("alpha", "gov.xml", "test"); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["name"] != "alpha" || payload["governanceXml"] != "<governance/>" || payload["description"] != "test" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !strings.Contains(out.String(), "ces-alpha-123") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestCreateEdgeSystemError(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems": newTextResponse(http.StatusBadRequest, `{"error":"name is required"}`),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(string) ([]byte, error) { return []byte(""), nil }
	if err := runner.CreateEdgeSystem("", "gov.xml", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Error") {
		t.Fatalf("expected error output: %s", out.String())
	}
}

func TestQueryEdgeSystem(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha": newJSONResponse(http.StatusOK, map[string]any{"name": "alpha", "status": "active"}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.QueryEdgeSystem("alpha"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "alpha") || !strings.Contains(out.String(), "active") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeleteEdgeSystem(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"DELETE /edge-systems/alpha": newTextResponse(http.StatusOK, `{"message":"deleted"}`),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.DeleteEdgeSystem("alpha"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "deleted successfully") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

// ── Edge Participant Tests ───────────────────────────────────────────────────

func TestCreateParticipant(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems/alpha/participants": newJSONResponse(http.StatusCreated, map[string]any{
			"participant_id": "sensor-net",
			"name":           "sensor-net",
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		if path != "perms.xml" {
			t.Fatalf("unexpected permissions file path: %s", path)
		}
		return []byte("<permissions/>"), nil
	}
	if err := runner.CreateParticipant("alpha", "sensor-net", "perms.xml", 3600); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["name"] != "sensor-net" || payload["permissionsXml"] != "<permissions/>" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !strings.Contains(out.String(), "sensor-net") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestListParticipants(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/participants": newJSONResponse(http.StatusOK, map[string]any{
			"participants": []any{map[string]any{"name": "sensor-net"}},
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListParticipants("alpha"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sensor-net") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestQueryParticipant(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/participants/sensor-net": newJSONResponse(http.StatusOK, map[string]any{
			"name": "sensor-net", "participant_id": "sensor-net",
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.QueryParticipant("alpha", "sensor-net"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sensor-net") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeleteParticipant(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"DELETE /edge-systems/alpha/participants/sensor-net": newTextResponse(http.StatusOK, "{}"),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.DeleteParticipant("alpha", "sensor-net"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Participant 'sensor-net' deleted") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

// ── Edge Campaign Tests ──────────────────────────────────────────────────────

func TestCreateCampaign(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems/alpha/participants/sensor-net/campaigns": newJSONResponse(http.StatusCreated, map[string]any{
			"campaign_id": "camp-123",
			"written":     2,
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte(`[{"serial":"SN001","macs":["AA:BB:CC:DD:EE:FF"]}]`), nil
	}
	if err := runner.CreateCampaign("alpha", "sensor-net", "devices.json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "camp-123") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestCreateCampaignCSV(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems/alpha/participants/sensor-net/campaigns": newJSONResponse(http.StatusCreated, map[string]any{
			"campaign_id": "camp-456",
			"written":     3,
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte("SN-001,\"AA:BB:CC:DD:EE:11,11:22:33:44:55:11\",pump-sensor\nSN-002,\"AA:BB:CC:DD:EE:22,11:22:33:44:55:22,11:22:33:44:22:22\",pump-sensor2\nSN-003,AA:BB:CC:DD:EE:33\n"), nil
	}
	if err := runner.CreateCampaign("alpha", "sensor-net", "devices.csv"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "camp-456") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestListCampaigns(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/participants/sensor-net/campaigns": newJSONResponse(http.StatusOK, map[string]any{
			"campaigns": []any{map[string]any{"campaign_id": "camp-123"}},
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListCampaigns("alpha", "sensor-net"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "camp-123") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestListCampaignDevices(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/participants/sensor-net/campaigns/camp-123/devices": newJSONResponse(http.StatusOK, map[string]any{
			"devices": []any{map[string]any{"serial": "SN001", "status": "pending"}},
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListCampaignDevices("alpha", "sensor-net", "camp-123"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SN001") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeleteCampaign(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"DELETE /edge-systems/alpha/participants/sensor-net/campaigns/camp-123": newTextResponse(http.StatusOK, "{}"),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.DeleteCampaign("alpha", "sensor-net", "camp-123"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Campaign 'camp-123' deleted") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

// ── Edge Device Tests ────────────────────────────────────────────────────────

func TestListEdgeDevices(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/devices": newJSONResponse(http.StatusOK, map[string]any{
			"devices": []any{map[string]any{"serial": "SN001"}},
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListEdgeDevices("alpha"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SN001") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRevokeDevice(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"DELETE /edge-systems/alpha/participants/sensor-net/campaigns/camp-123/devices/SN001": newTextResponse(http.StatusOK, "{}"),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.RevokeDevice("alpha", "sensor-net", "camp-123", "SN001"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Device 'SN001' revoked") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestEnrollDevice(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems/ces-alpha-123/participants/sensor-net/enroll": newJSONResponse(http.StatusOK, map[string]any{
			"certificate": "-----BEGIN CERTIFICATE-----\nMIIB...",
			"ca_chain":    "-----BEGIN CERTIFICATE-----\nMIIC...",
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte("-----BEGIN CERTIFICATE REQUEST-----\ntest\n-----END CERTIFICATE REQUEST-----"), nil
	}
	if err := runner.EnrollDevice("ces-alpha-123", "sensor-net", "SN001", []string{"AA:BB:CC:DD:EE:FF"}, "device.csr", ""); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["serial"] != "SN001" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !strings.Contains(out.String(), "certificate") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}
