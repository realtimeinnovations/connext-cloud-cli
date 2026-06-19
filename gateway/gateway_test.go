package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

func TestRoutingStateTracksWildcardRouteInstances(t *testing.T) {
	state := NewRoutingState(RoutingLiveLogLines)
	lines := []string{
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc|STREAM_DISCOVERED] name=Square, type_name=ShapeType, connection=edge_participant_domain_0",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/auto_routes/route_0_all_edge_to_cloud|MATCH STREAM|../../routes/route_0_all_edge_to_cloud@Square|CREATE]",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc/sessions/default_session/routes/route_0_all_edge_to_cloud@Square/inputs/Input1|ENABLE] stream=Square",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc/sessions/default_session/routes/route_0_all_edge_to_cloud@Square/outputs/Output1|ENABLE] stream=Square",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc/sessions/default_session/routes/route_0_all_edge_to_cloud@Square|RUN]",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc|STREAM_DISCOVERED] name=Circle, type_name=ShapeType, connection=edge_participant_domain_0",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/auto_routes/route_0_all_edge_to_cloud|MATCH STREAM|../../routes/route_0_all_edge_to_cloud@Circle|CREATE]",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc/sessions/default_session/routes/route_0_all_edge_to_cloud@Circle/inputs/Input1|ENABLE] stream=Circle",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc/sessions/default_session/routes/route_0_all_edge_to_cloud@Circle/outputs/Output1|ENABLE] stream=Circle",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc/sessions/default_session/routes/route_0_all_edge_to_cloud@Circle|RUN]",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc|STREAM_DISCOVERED] name=Square, type_name=ShapeType, connection=cloud",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/auto_routes/route_0_all_cloud_to_edge|MATCH STREAM|../../routes/route_0_all_cloud_to_edge@Square|CREATE]",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_cloud_to_edge@Square] Input1 matched publication stream=Square",
		"LOCAL [/routing_services/all_gateway_gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_cloud_to_edge@Square|ENABLE]",
	}
	for _, line := range lines {
		state.Update(line)
	}
	routes := state.Routes()
	squareEdge := routes["route_0_all_edge_to_cloud@Square"]
	circleEdge := routes["route_0_all_edge_to_cloud@Circle"]
	squareCloud := routes["route_0_all_cloud_to_edge@Square"]
	if squareEdge.State != "RUN" || squareEdge.Direction != "edge_to_cloud" || !squareEdge.InputMatched || !squareEdge.OutputMatched {
		t.Fatalf("unexpected Square edge route: %#v", squareEdge)
	}
	if circleEdge.State != "RUN" || !circleEdge.InputMatched || !circleEdge.OutputMatched {
		t.Fatalf("unexpected Circle edge route: %#v", circleEdge)
	}
	if squareCloud.Direction != "cloud_to_edge" || !squareCloud.InputMatched {
		t.Fatalf("unexpected Square cloud route: %#v", squareCloud)
	}
	rows := topicRowsByTopic(state.TopicRows())
	if rows["Square"].EdgeToCloud != "live" || rows["Square"].CloudToEdge != "ready" || rows["Square"].TypeName != "ShapeType" {
		t.Fatalf("unexpected Square row: %#v", rows["Square"])
	}
	if rows["Circle"].EdgeToCloud != "live" {
		t.Fatalf("unexpected Circle row: %#v", rows["Circle"])
	}
}

func TestPlainEventLinesFormatsGatewayEvents(t *testing.T) {
	lines := []string{
		"RTI Routing Service 7.7.0 executing (with configuration=gw)",
		"LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED] name=Square, type_name=ShapeType, connection=edge_participant_domain_0",
		"LOCAL [/routing_services/gateway/domain_routes/etc/sessions/default_session/routes/route_0_all_edge_to_cloud@Square|RUN]",
		"WARNING [/routing_services/gateway/domain_routes/etc] Port 7410 in use",
	}
	got := []string{}
	for _, line := range lines {
		got = append(got, PlainEventLines(line)...)
	}
	expected := []string{
		"[status] running",
		"[stream] Square ShapeType discovered from edge_participant_domain_0",
		"[route] Square edge_to_cloud run",
		"[warning] Port 7410 in use",
	}
	if strings.Join(got, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("unexpected lines:\n%s", strings.Join(got, "\n"))
	}
}

func TestRoutingStateFiltersXMLNoiseFromLiveLog(t *testing.T) {
	state := NewRoutingState(5)
	state.Update("LOCAL [/routing_services/gw|CREATE] RTIXMLUTILSTransformer_transformWithParams:=== Transformed XML:")
	state.Update("<dds>")
	state.Update("WARNING [/routing_services/gw|START] Port 7410 in use")
	logs := state.RecentLogs()
	if len(logs) != 1 || logs[0] != "WARNING: Port 7410 in use" {
		t.Fatalf("unexpected logs: %#v", logs)
	}
}

func TestRoutingStateDoesNotShowDiscoveryWithoutRoute(t *testing.T) {
	state := NewRoutingState(RoutingLiveLogLines)
	state.Update("LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED] name=Square, type_name=ShapeType, connection=cloud")
	if rows := state.TopicRows(); len(rows) != 0 {
		t.Fatalf("expected no rows, got %#v", rows)
	}
}

func TestGatewayLiveHeaderAndResources(t *testing.T) {
	config := map[string]any{
		"databus":       "db",
		"observability": "obs",
		"templates": map[string]any{
			"gateway":   "gw",
			"collector": "collector",
		},
	}
	header := GatewayLiveHeader(config, "running, waiting for discovered topics", 0, 0)
	resources := GatewayLiveResources(config, "running")
	if header.Label != "databus" || !strings.Contains(header.Status, "waiting topics") || header.Target != "db / gw" {
		t.Fatalf("unexpected header: %#v", header)
	}
	if GatewayPanelTitle() != "[bold] Connext Cloud Gateway  [/bold]" {
		t.Fatalf("unexpected panel title")
	}
	if resources.Label != "observability" || !strings.Contains(resources.Status, "running") || resources.Target != "obs / collector" {
		t.Fatalf("unexpected resources: %#v", resources)
	}
}

func TestGatewayLiveHeaderShowsRoutedTopicCount(t *testing.T) {
	header := GatewayLiveHeader(map[string]any{"databus": "db", "templates": map[string]any{"gateway": "gw"}}, "running", 1, 0)
	if !strings.Contains(header.Status, "routing 1 topic") {
		t.Fatalf("unexpected header status: %s", header.Status)
	}
}

func TestRouteSummaryUsesSingleIOStatusChip(t *testing.T) {
	cases := []struct {
		edgeToCloud string
		cloudToEdge string
		expected    string
	}{
		{"live", "live", "[green]↑↓[/green]"},
		{"live", "waiting", "[green]↑·[/green]"},
		{"waiting", "live", "[green]↓·[/green]"},
		{"live", "stopped", "[green]↑·[/green]"},
		{"stopped", "live", "[green]↓·[/green]"},
		{"ready", "ready", "[#5f819d]○[/]"},
		{"stopped", "ready", "[dim]◌[/dim]"},
		{"starting", "stopped", "[#5f819d]◐[/]"},
	}
	for _, tc := range cases {
		row := TopicRouteRow{Topic: "Circle", TypeName: "ShapeType", EdgeToCloud: tc.edgeToCloud, CloudToEdge: tc.cloudToEdge}
		if got := RouteStatusChip(row, 0); got != tc.expected {
			t.Fatalf("edge=%s cloud=%s expected %s got %s", tc.edgeToCloud, tc.cloudToEdge, tc.expected, got)
		}
	}
}

func TestRouteSummaryLiveChipAnimatesDuplexArrows(t *testing.T) {
	row := TopicRouteRow{Topic: "Circle", TypeName: "ShapeType", EdgeToCloud: "live", CloudToEdge: "live"}
	if got := RouteStatusChip(row, 1); got != "[green]↓↑[/green]" {
		t.Fatalf("unexpected frame 1: %s", got)
	}
	if got := RouteStatusChip(row, 2); got != "[green]↑↓[/green]" {
		t.Fatalf("unexpected frame 2: %s", got)
	}
}

func TestTopicStatusLabelSummarizesDirection(t *testing.T) {
	if got := TopicStatusLabel(TopicRouteRow{Topic: "docs", EdgeToCloud: "live", CloudToEdge: "waiting"}); got != "routing upstream" {
		t.Fatalf("unexpected upstream label: %s", got)
	}
	if got := TopicStatusLabel(TopicRouteRow{Topic: "docs", EdgeToCloud: "waiting", CloudToEdge: "live"}); got != "routing downstream" {
		t.Fatalf("unexpected downstream label: %s", got)
	}
	if got := TopicStatusLabel(TopicRouteRow{Topic: "docs", EdgeToCloud: "live", CloudToEdge: "live"}); got != "routing both" {
		t.Fatalf("unexpected both label: %s", got)
	}
}

func TestRoutingStateSetsEndpointSpecificTopicStatuses(t *testing.T) {
	state := NewRoutingState(RoutingLiveLogLines)
	lines := []string{
		"LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_edge_to_cloud@Square] Input1 matched publication stream=Square",
		"LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_edge_to_cloud@Square] Output1 matched subscription stream=Square",
		"LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_cloud_to_edge@Circle] Input1 matched publication stream=Circle",
		"LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_cloud_to_edge@Circle] Output1 matched subscription stream=Circle",
	}
	for _, line := range lines {
		state.Update(line)
	}
	rows := topicRowsByTopic(state.TopicRows())
	if rows["Square"].LastEvent != "cloud subscriber found" {
		t.Fatalf("unexpected Square status: %#v", rows["Square"])
	}
	if rows["Circle"].LastEvent != "edge subscriber found" {
		t.Fatalf("unexpected Circle status: %#v", rows["Circle"])
	}
}

func TestRoutingStatePreservesProblemStatusOverEndpointMatch(t *testing.T) {
	state := NewRoutingState(RoutingLiveLogLines)
	lines := []string{
		"WARNING [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_cloud_to_edge@PingTopic] ROUTERStreamPort_provideTypeInformation:FAILED TO GET | Type information does not provide a type representation",
		"LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_cloud_to_edge@PingTopic] Input1 matched publication stream=PingTopic",
		"LOCAL [/routing_services/gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/route_0_all_cloud_to_edge@PingTopic|ENABLE]",
	}
	for _, line := range lines {
		state.Update(line)
	}
	rows := state.TopicRows()
	if len(rows) != 1 || rows[0].LastEvent != "type info missing" {
		t.Fatalf("unexpected rows: %#v", rows)
	}
}

func TestSortTopicRowsForDisplay(t *testing.T) {
	rows := []TopicRouteRow{
		{Topic: "Command", EdgeToCloud: "waiting", CloudToEdge: "waiting"},
		{Topic: "Triangle", EdgeToCloud: "stopped", CloudToEdge: "waiting"},
		{Topic: "Alex", EdgeToCloud: "stopped", CloudToEdge: "ready"},
		{Topic: "Square", EdgeToCloud: "live", CloudToEdge: "waiting"},
		{Topic: "Circle", EdgeToCloud: "live", CloudToEdge: "live"},
	}
	sortedRows := SortTopicRowsForDisplay(rows)
	got := []string{sortedRows[0].Topic, sortedRows[1].Topic, sortedRows[2].Topic, sortedRows[3].Topic, sortedRows[4].Topic}
	expected := []string{"Circle", "Square", "Alex", "Triangle", "Command"}
	for idx := range expected {
		if got[idx] != expected[idx] {
			t.Fatalf("unexpected order: %#v", got)
		}
	}
}

func TestVisibleTopicRowsHideWildcardWhenConcreteExists(t *testing.T) {
	rows := []TopicRouteRow{{Topic: "*", EdgeToCloud: "listening"}, {Topic: "acme/docs", EdgeToCloud: "live"}}
	visible := VisibleTopicRows(rows)
	if len(visible) != 1 || visible[0].Topic != "acme/docs" {
		t.Fatalf("unexpected rows: %#v", visible)
	}
}

func TestVisibleTopicRowsKeepWildcardUntilConcreteExists(t *testing.T) {
	rows := []TopicRouteRow{{Topic: "*", EdgeToCloud: "listening"}}
	visible := VisibleTopicRows(rows)
	if len(visible) != 1 || visible[0].Topic != "*" {
		t.Fatalf("unexpected rows: %#v", visible)
	}
}

func TestRoutingStateTracksQuotedWildcardRouteWithSlashTopic(t *testing.T) {
	state := NewRoutingState(RoutingLiveLogLines)
	lines := []string{
		"LOCAL [/routing_services/acme_gateway/domain_routes/etc|STREAM_DISCOVERED] name=acme/docs, type_name=PingType, connection=edge_participant_domain_0",
		`LOCAL [/routing_services/acme_gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/auto_routes/route_0_acme*_edge_to_cloud|MATCH STREAM|../../routes/"route_0_acme*_edge_to_cloud@acme/docs"|CREATE]`,
		`LOCAL [/routing_services/acme_gateway/domain_routes/etc|STREAM_DISCOVERED|/sessions/default_session|/routes/"route_0_acme*_edge_to_cloud@acme/docs"] Input1 matched publication stream=acme/docs`,
		`LOCAL [/routing_services/acme_gateway/domain_routes/etc/sessions/default_session/routes/"route_0_acme*_edge_to_cloud@acme/docs"/inputs/Input1|ENABLE] stream=acme/docs`,
		`LOCAL [/routing_services/acme_gateway/domain_routes/etc/sessions/default_session/routes/"route_0_acme*_edge_to_cloud@acme/docs"/outputs/Output1|ENABLE] stream=acme/docs`,
		`LOCAL [/routing_services/acme_gateway/domain_routes/etc/sessions/default_session/routes/"route_0_acme*_edge_to_cloud@acme/docs"|START]`,
		`LOCAL [/routing_services/acme_gateway/domain_routes/etc/sessions/default_session/routes/"route_0_acme*_edge_to_cloud@acme/docs"|RUN]`,
	}
	for _, line := range lines {
		state.Update(line)
	}
	routes := state.Routes()
	route := routes["route_0_acme*_edge_to_cloud@acme/docs"]
	row := state.TopicRows()[0]
	if route.State != "RUN" || !route.InputMatched || !route.OutputMatched {
		t.Fatalf("unexpected route: %#v", route)
	}
	if row.Topic != "acme/docs" || row.TypeName != "PingType" || row.EdgeToCloud != "live" || row.CloudToEdge != "waiting" {
		t.Fatalf("unexpected row: %#v", row)
	}
}

func TestRoutingStateSeedsListeningRoutesFromGatewayXML(t *testing.T) {
	tmpDir := t.TempDir()
	xmlPath := filepath.Join(tmpDir, "gateway.xml")
	if err := os.WriteFile(xmlPath, []byte(`
        <dds>
          <routing_service name="gateway">
            <domain_route name="etc">
              <session name="default_session">
                <auto_topic_route name="route_0_all_edge_to_cloud">
                  <input participant="edge">
                    <allow_topic_name_filter>*</allow_topic_name_filter>
                  </input>
                  <output participant="cloud"/>
                </auto_topic_route>
              </session>
            </domain_route>
          </routing_service>
        </dds>
    `), 0o644); err != nil {
		t.Fatal(err)
	}
	state := NewRoutingState(RoutingLiveLogLines)
	state.SeedFromConfig(xmlPath)
	state.Update("RTI Routing Service 7.6.0 executing (with configuration=gateway)")
	route := state.Routes()["route_0_all_edge_to_cloud@*"]
	if route.State != "LISTENING" || route.Direction != "edge_to_cloud" || state.ServiceState() != "running" {
		t.Fatalf("unexpected route/state: %#v %s", route, state.ServiceState())
	}
	row := state.TopicRows()[0]
	if row.Topic != "*" || row.EdgeToCloud != "listening" || row.CloudToEdge != "waiting" || row.LastEvent != "listening" {
		t.Fatalf("unexpected row: %#v", row)
	}
}

func TestDashboardURLUsesTelemetryServicesForObservability(t *testing.T) {
	if got := DashboardURL("dev-cloud", "luis-secobs-77", "observability"); got != "https://test.cloud.dev-rti.com/dashboard/observability-services/luis-secobs-77" {
		t.Fatalf("unexpected URL: %s", got)
	}
	if got := DashboardURL("dev-cloud", "alex-test", "databus"); got != "https://test.cloud.dev-rti.com/dashboard/databuses/alex-test" {
		t.Fatalf("unexpected URL: %s", got)
	}
}

func TestAPIConnectionErrorReportsConfiguredHost(t *testing.T) {
	errMsg := FormatAPIConnectionError("GET", "http://localhost:8090", "/databuses?extra_fields=true", GatewayError{Message: "connection refused"})
	checks := []string{
		"Cannot reach Connext Cloud API.",
		"Configured API host:\n  http://localhost:8090",
		"rticloud configure --region us-west-2",
		"GET /databuses?extra_fields=true failed: connection refused",
	}
	for _, check := range checks {
		if !strings.Contains(errMsg, check) {
			t.Fatalf("missing %q in %s", check, errMsg)
		}
	}
}

func TestAPIConnectionErrorReportsNotConfiguredHost(t *testing.T) {
	errMsg := FormatAPIConnectionError("GET", "", "/databuses?extra_fields=true", GatewayError{Message: "Please run 'rticloud configure' first."})
	checks := []string{
		"Configured API host:\n  not configured",
		"Configure the local API host, then start or port-forward the management API.",
		"GET /databuses?extra_fields=true failed: Please run 'rticloud configure' first.",
	}
	for _, check := range checks {
		if !strings.Contains(errMsg, check) {
			t.Fatalf("missing %q in %s", check, errMsg)
		}
	}
}

func TestDiscoverConnextUsesNDDSHOME(t *testing.T) {
	tmpDir := t.TempDir()
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(filepath.Join(install, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := DiscoverConnextInstall(map[string]string{"NDDSHOME": install})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "7.7.0" || result.Path == "" {
		t.Fatalf("unexpected install: %#v", result)
	}
	if result.Reason != "selected via $NDDSHOME" {
		t.Fatalf("unexpected reason: %q", result.Reason)
	}
}

func TestDiscoverConnextUsesCONNEXTDDS_DIR(t *testing.T) {
	tmpDir := t.TempDir()
	install := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(filepath.Join(install, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := DiscoverConnextInstall(map[string]string{"CONNEXTDDS_DIR": install})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "7.7.0" || result.Path == "" {
		t.Fatalf("unexpected install: %#v", result)
	}
	if result.Reason != "selected via $CONNEXTDDS_DIR" {
		t.Fatalf("unexpected reason: %q", result.Reason)
	}
}

func TestDiscoverConnextSelectsHighestCommonInstallWhenNotPrompting(t *testing.T) {
	tmpDir := t.TempDir()
	older := filepath.Join(tmpDir, "rti_connext_dds-7.6.0")
	newer := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(filepath.Join(older, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(newer, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousPatterns := append([]string(nil), connext.InstallPatterns...)
	t.Cleanup(func() { connext.InstallPatterns = previousPatterns })
	connext.InstallPatterns = []string{filepath.Join(tmpDir, "rti_connext_dds-*")}
	t.Cleanup(func() {
	})
	if err := os.WriteFile(filepath.Join(older, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newer, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := DiscoverConnextInstallWithPrompt(map[string]string{}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "7.7.0" {
		t.Fatalf("unexpected install: %#v", result)
	}
}

func TestDiscoverConnextPromptsForExistingInstallations(t *testing.T) {
	tmpDir := t.TempDir()
	older := filepath.Join(tmpDir, "rti_connext_dds-7.6.0")
	newer := filepath.Join(tmpDir, "rti_connext_dds-7.7.0")
	if err := os.MkdirAll(filepath.Join(older, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(newer, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	previousPatterns := append([]string(nil), connext.InstallPatterns...)
	t.Cleanup(func() { connext.InstallPatterns = previousPatterns })
	connext.InstallPatterns = []string{filepath.Join(tmpDir, "rti_connext_dds-*")}
	if err := os.WriteFile(filepath.Join(older, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newer, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	var gotMessage string
	var gotChoices []string
	result, err := DiscoverConnextInstallWithPrompt(map[string]string{}, true, func(message string, choices []string) (string, error) {
		gotMessage = message
		gotChoices = append([]string(nil), choices...)
		return older, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotMessage != "Select Connext installation:" {
		t.Fatalf("unexpected prompt: %s", gotMessage)
	}
	if len(gotChoices) != 4 || gotChoices[0] != newer || gotChoices[1] != older || gotChoices[2] != connext.EnterConnextPathLabel || gotChoices[3] != connext.DownloadConnextLabel {
		t.Fatalf("unexpected choices: %#v", gotChoices)
	}
	if result.Path != older {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestValidateConnextRejectsOldVersion(t *testing.T) {
	tmpDir := t.TempDir()
	install := filepath.Join(tmpDir, "rti_connext_dds-7.2.0")
	if err := os.MkdirAll(filepath.Join(install, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(install, "bin", "rtiroutingservice"), []byte(""), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ValidateConnextInstall(install)
	if err == nil || !strings.Contains(err.Error(), "requires Connext Pro 7.3.0 or newer") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRoutingLiveViewUsesOrangeBorderAndGatewayRows(t *testing.T) {
	view := NewRoutingLiveView(map[string]any{
		"databus":       "db",
		"observability": "obs",
		"templates": map[string]any{
			"gateway":   "gw",
			"collector": "collector",
		},
	})
	view.CollectorStatusFunc = func(config map[string]any, collectorName string) string { return "running" }
	view.DatabusSecure = false
	view.CollectorSecure = true
	view.State.serviceState = "running"
	view.State.routes[routeKey{Route: "route_0_docs_edge_to_cloud", Topic: "docs"}] = &RouteState{
		Route:     "route_0_docs_edge_to_cloud",
		Topic:     "docs",
		Direction: "edge_to_cloud",
		State:     "RUN",
	}
	layout := view.Render(0)
	if layout.Border != tui.RTIOrange || layout.Title != GatewayPanelTitle() {
		t.Fatalf("unexpected layout shell: %#v", layout)
	}
	if layout.Header.Label != "databus" || !strings.Contains(layout.Header.Status, "routing 1 topic") || layout.Header.Target != "db / gw" {
		t.Fatalf("unexpected header: %#v", layout.Header)
	}
	if layout.Header.Warning != "not secure" {
		t.Fatalf("expected header warning 'not secure', got %q", layout.Header.Warning)
	}
	if layout.Resource.Label != "observability" || !strings.Contains(layout.Resource.Status, "running") || layout.Resource.Target != "obs / collector" {
		t.Fatalf("unexpected resources: %#v", layout.Resource)
	}
	if layout.Resource.Warning != "secure" {
		t.Fatalf("expected resource warning 'secure', got %q", layout.Resource.Warning)
	}
	if len(layout.Routes) != 1 || layout.Routes[0].IO != "[green]↑·[/green]" || layout.Routes[0].Topic != "docs" || layout.Routes[0].Status != "routing upstream" {
		t.Fatalf("unexpected routes: %#v", layout.Routes)
	}
}

func TestRenderANSIAvoidsFullScreenClear(t *testing.T) {
	view := RenderedView{
		Title:    GatewayPanelTitle(),
		Header:   RenderedSummaryLine{Label: "databus", Status: "[green]● running[/green]", Target: "db / gw"},
		Resource: RenderedSummaryLine{Label: "observability", Status: "[dim]◌ not configured[/dim]", Target: "obs / collector"},
	}
	rendered := renderANSI(view)
	if !strings.HasPrefix(rendered, "\x1b[H\x1b[J") {
		t.Fatalf("expected cursor-home redraw prefix, got %q", rendered[:minInt(len(rendered), 8)])
	}
	if strings.HasPrefix(rendered, "\x1b[2J") {
		t.Fatalf("expected renderer to avoid full-screen clear, got %q", rendered[:minInt(len(rendered), 8)])
	}
}

func TestRenderSetupIntroIncludesWelcomeBoxAndHint(t *testing.T) {
	rendered := tui.StripANSIEscapes(RenderSetupIntro(2, 1, true))
	checks := []string{"Connext Cloud Gateway setup", "Databuses available: 2", "Observability services: 1", "Use arrow keys to choose and Enter to confirm."}
	for _, check := range checks {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing %q in %s", check, rendered)
		}
	}
}

func TestRenderANSIForSizeKeepsSummaryVisibleInShortTerminal(t *testing.T) {
	view := RenderedView{
		Title:    GatewayPanelTitle(),
		Header:   RenderedSummaryLine{Label: "databus", Status: "[green]● routing 4 topics[/green]", Target: "db / gw"},
		Resource: RenderedSummaryLine{Label: "observability", Status: "[dim]◌ not configured[/dim]", Target: "none / none"},
		Routes:   make([]RenderedRoute, 0, 16),
		LogLines: make([]string, 0, 18),
	}
	for index := 0; index < 16; index++ {
		view.Routes = append(view.Routes, RenderedRoute{IO: "[green]↑·[/green]", Topic: fmt.Sprintf("topic-%02d", index), Type: "PingType", Status: "routing upstream"})
	}
	for index := 0; index < 18; index++ {
		view.LogLines = append(view.LogLines, strings.Repeat(fmt.Sprintf("log-%02d ", index), 16))
	}
	rendered := tui.StripANSIEscapes(renderANSIForSize(view, 60, 20))
	lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	if len(lines) > 20 {
		t.Fatalf("render exceeded terminal height: %d lines\n%s", len(lines), rendered)
	}
	checks := []string{"Connext Cloud Gateway", "DATABUS", "OBSERVABILITY", "Routes"}
	for _, check := range checks {
		if !strings.Contains(rendered, check) {
			t.Fatalf("missing %q in rendered output: %s", check, rendered)
		}
	}
	if strings.Contains(rendered, strings.Repeat("log-17 ", 16)) || !strings.Contains(rendered, "…") {
		t.Fatalf("expected wrapped log line to be truncated: %s", rendered)
	}
}

func topicRowsByTopic(rows []TopicRouteRow) map[string]TopicRouteRow {
	result := map[string]TopicRouteRow{}
	for _, row := range rows {
		result[row.Topic] = row
	}
	return result
}
