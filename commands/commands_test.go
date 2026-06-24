// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

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
		"GET /databuses/inventory":   newJSONResponse(http.StatusOK, map[string]any{"status": "active"}),
		"GET /databuses/inventory-2": newJSONResponse(http.StatusOK, map[string]any{"status": "active"}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.Sleep = func(time.Duration) {}
	if err := runner.CreateDatabus("inventory", 2, "", "", true); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["secure"] != true {
		t.Fatalf("expected secure databus payload, got %#v", payload)
	}
	output := out.String()
	if !strings.Contains(output, "Waiting for creation to complete") || !strings.Contains(output, "Databus status:  active") {
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
	err := runner.CreateDatabus("inventory", 2, "", "", true)
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for databus \"inventory\" to leave \"creating\"") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateDatabusSupportsNonSecurePayload(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /databuses":          newTextResponse(http.StatusCreated, "{}"),
		"GET /databuses/inventory": newJSONResponse(http.StatusOK, map[string]any{"status": "active"}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.Sleep = func(time.Duration) {}
	if err := runner.CreateDatabus("inventory", 2, "", "", false); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["secure"] != false {
		t.Fatalf("expected non-secure databus payload, got %#v", payload)
	}
}

func TestCreateObsServiceSecureByDefault(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /databuses":    newTextResponse(http.StatusCreated, "{}"),
		"GET /databuses/obs": newJSONResponse(http.StatusOK, map[string]any{"status": "active"}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.Sleep = func(time.Duration) {}
	if err := runner.CreateObsService("obs", "network-a", true); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["secure"] != true || payload["network_name"] != "network-a" || payload["enable_edge_observability"] != true {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestCreateObsServiceSupportsNonSecurePayload(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /databuses":    newTextResponse(http.StatusCreated, "{}"),
		"GET /databuses/obs": newJSONResponse(http.StatusOK, map[string]any{"status": "active"}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.Sleep = func(time.Duration) {}
	if err := runner.CreateObsService("obs", "", false); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["secure"] != false {
		t.Fatalf("expected non-secure observability payload, got %#v", payload)
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

func TestDownloadLicenseReturnsBody(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /licenses": newTextResponse(http.StatusOK, "license-body")}}
	runner := New(api, io.Discard)
	days := 14
	body, err := runner.DownloadLicense(&days)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "license-body" {
		t.Fatalf("unexpected license content: %s", body)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["expiration_days"] != days {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestDownloadLicenseReturnsAPIError(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{"POST /licenses": newTextResponse(http.StatusBadRequest, `{"error":"complete your profile"}`)}}
	runner := New(api, io.Discard)
	_, err := runner.DownloadLicense(nil)
	if err == nil || !strings.Contains(err.Error(), "complete your profile") {
		t.Fatalf("unexpected error: %v", err)
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
	if err := runner.CreateEdgeSystem("alpha", "test"); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["name"] != "alpha" || payload["description"] != "test" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if _, hasGov := payload["governanceXml"]; hasGov {
		t.Fatal("governanceXml must not be sent in create request")
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
	if err := runner.CreateEdgeSystem("", ""); err != nil {
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

// ── Edge Participant Template Tests ──────────────────────────────────────────

func TestCreateParticipantTemplate(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems/alpha/participant-templates": newJSONResponse(http.StatusCreated, map[string]any{
			"participant_id": "sensor-net-a1b2c3d4",
			"name":           "sensor-net",
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.CreateParticipantTemplate("alpha", "sensor-net", "permissions-allow-all-v1", 1440); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["name"] != "sensor-net" || payload["permissionsRef"] != "permissions-allow-all-v1" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !strings.Contains(out.String(), "sensor-net") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestListParticipantTemplates(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/participant-templates": newJSONResponse(http.StatusOK, map[string]any{
			"participant_templates": []any{map[string]any{"name": "sensor-net"}},
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListParticipantTemplates("alpha"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sensor-net") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestGetParticipantTemplate(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/participant-templates/sensor-net": newJSONResponse(http.StatusOK, map[string]any{
			"name": "sensor-net", "participant_id": "sensor-net-a1b2c3d4",
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.GetParticipantTemplate("alpha", "sensor-net"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sensor-net") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeleteParticipantTemplate(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"DELETE /edge-systems/alpha/participant-templates/sensor-net": newTextResponse(http.StatusOK, "{}"),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.DeleteParticipantTemplate("alpha", "sensor-net"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Participant template 'sensor-net' deleted") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

// ── Edge Campaign Tests ──────────────────────────────────────────────────────

func TestCreateCampaign(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems/alpha/campaigns": newJSONResponse(http.StatusCreated, map[string]any{
			"campaign_id": "camp-123",
			"written":     2,
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte(`[{"serial":"SN001","macs":["AA:BB:CC:DD:EE:FF"]}]`), nil
	}
	if err := runner.CreateCampaign("alpha", "sensor-net", "devices.json", "edge-default-tag"); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["domainTemplateId"] != "edge-default-tag" {
		t.Fatalf("expected domainTemplateId in payload, got: %#v", payload)
	}
	if payload["participantTemplateId"] != "sensor-net" {
		t.Fatalf("expected participantTemplateId in payload, got: %#v", payload)
	}
	if !strings.Contains(out.String(), "camp-123") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestCreateCampaignCSV(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"POST /edge-systems/alpha/campaigns": newJSONResponse(http.StatusCreated, map[string]any{
			"campaign_id": "camp-456",
			"written":     3,
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte("SN-001,\"AA:BB:CC:DD:EE:11,11:22:33:44:55:11\",pump-sensor\nSN-002,\"AA:BB:CC:DD:EE:22,11:22:33:44:55:22,11:22:33:44:22:22\",pump-sensor2\nSN-003,AA:BB:CC:DD:EE:33\n"), nil
	}
	if err := runner.CreateCampaign("alpha", "sensor-net", "devices.csv", "edge-default-tag"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "camp-456") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestListCampaigns(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/campaigns": newJSONResponse(http.StatusOK, map[string]any{
			"campaigns": []any{map[string]any{"campaign_id": "camp-123"}},
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListCampaigns("alpha"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "camp-123") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestListCampaignDevices(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"GET /edge-systems/alpha/campaigns/camp-123/devices": newJSONResponse(http.StatusOK, map[string]any{
			"devices": []any{map[string]any{"serial": "SN001", "status": "pending"}},
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.ListCampaignDevices("alpha", "camp-123"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SN001") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDeleteCampaign(t *testing.T) {
	api := &fakeAPI{responses: map[string]*http.Response{
		"DELETE /edge-systems/alpha/campaigns/camp-123": newTextResponse(http.StatusOK, "{}"),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	if err := runner.DeleteCampaign("alpha", "camp-123"); err != nil {
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
		"POST /edge-systems/ces-alpha-123/enroll": newJSONResponse(http.StatusOK, map[string]any{
			"certificate":    "-----BEGIN CERTIFICATE-----\nMIIB...",
			"caChain":        "-----BEGIN CERTIFICATE-----\nMIIC...",
			"governanceP7s":  "MIME-Version: 1.0\ncontent",
			"participant_id": "sensor-net",
		}),
	}}
	var out bytes.Buffer
	runner := New(api, &out)
	runner.ReadFile = func(path string) ([]byte, error) {
		return []byte("-----BEGIN CERTIFICATE REQUEST-----\ntest\n-----END CERTIFICATE REQUEST-----"), nil
	}
	if _, err := runner.EnrollDevice("ces-alpha-123", "sensor-net", "SN001", []string{"AA:BB:CC:DD:EE:FF"}, "device.csr", "", ""); err != nil {
		t.Fatal(err)
	}
	payload := api.lastPayload.(map[string]any)
	if payload["serial"] != "SN001" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if !strings.Contains(out.String(), "certificate") || !strings.Contains(out.String(), "governanceP7s") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}
