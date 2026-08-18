// Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
// No duplications, whole or partial, manual or electronic, may be made
// without express written permission.  Any such copies, or revisions thereof,
// must display this notice unaltered.
// This code contains trade secrets of Real-Time Innovations, Inc.

package gateway

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/diagnostics"
	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

const (
	RoutingLiveRouteRows  = 16
	defaultTerminalWidth  = 120
	defaultTerminalHeight = 40
	summaryLabelMinWidth  = 4
	summaryLabelMaxWidth  = 14
	summaryStatusMinWidth = 1
	summaryStatusMaxWidth = 35
	routeIOWidth          = 4
	routeTopicMinWidth    = 20
	routeTopicMaxWidth    = 28
	routeTypeMinWidth     = 12
	routeTypeMaxWidth     = 18
)

type RenderedSummaryLine struct {
	Label   string
	Status  string
	Target  string
	Warning string
}

type RenderedRoute struct {
	IO     string
	Topic  string
	Type   string
	Status string
}

type RenderedView struct {
	Title           string
	Header          RenderedSummaryLine
	Resource        RenderedSummaryLine
	Routes          []RenderedRoute
	LogLines        []string
	LogTimes        []time.Time // parallel to LogLines; zero entries suppress the timestamp
	Border          string
	HideRoutes      bool
	LogTitle        string
	LogEmptyMessage string
	Findings        []diagnostics.Finding
}

type KeyValueRow struct {
	Key   string
	Value string
}

type RoutingLiveView struct {
	Config              map[string]any
	CollectorName       string
	DatabusSecure       bool
	CollectorSecure     bool
	CollectorDiscovery  CollectorDiscoveryState
	State               *RoutingState
	CollectorStatus     string
	CollectorStatusFunc func(config map[string]any, collectorName string) string
	LastSnapshot        RenderedView
	Enabled             bool
	Detector            *diagnostics.Detector
}

type CollectorDiscoveryState struct {
	Enabled          bool
	ServiceKnown     bool
	ServiceConnected bool
	EdgeAppsKnown    bool
	EdgeApps         int
}

var collectorDiscoveryEventRE = regexp.MustCompile(`(MonitoringEventReader_on_subscription_matched|MonitorEventWriter_on_publication_matched):(MATCH|UNMATCH)\b.*\btotal_matched = (\d+)`)

type TerminalRenderer struct {
	Out    io.Writer
	screen *tui.Screen
}

func NewRoutingLiveView(config map[string]any) *RoutingLiveView {
	return &RoutingLiveView{
		Config:          config,
		State:           NewRoutingState(RoutingLiveLogLines),
		CollectorStatus: "not configured",
		Enabled:         true,
		Detector:        diagnostics.NewDetector(),
	}
}

func (view *RoutingLiveView) HandleLine(line string) (diagnostics.Finding, bool) {
	view.State.Update(line)
	return view.Detector.Observe("routing", line)
}

func (view *RoutingLiveView) HandleCollectorLine(line string) (diagnostics.Finding, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return diagnostics.Finding{}, false
	}
	view.updateCollectorDiscovery(line)
	view.State.appendLog("collector " + trimmed)
	return view.Detector.Observe("collector", line)
}

func (view *RoutingLiveView) updateCollectorDiscovery(line string) bool {
	match := collectorDiscoveryEventRE.FindStringSubmatch(line)
	if match == nil {
		return false
	}
	totalMatched, err := strconv.Atoi(match[3])
	if err != nil {
		return false
	}
	view.CollectorDiscovery.Enabled = true
	if match[1] == "MonitorEventWriter_on_publication_matched" {
		view.CollectorDiscovery.ServiceKnown = true
		view.CollectorDiscovery.ServiceConnected = totalMatched > 0
		return true
	}
	view.CollectorDiscovery.EdgeAppsKnown = true
	view.CollectorDiscovery.EdgeApps = totalMatched
	return true
}

func (view *RoutingLiveView) SeedFromConfig(xmlPath string) {
	view.State.SeedFromConfig(xmlPath)
}

func (view *RoutingLiveView) PulseFrame(now ...float64) int {
	if len(now) > 0 {
		return LivePulseFrame(now[0])
	}
	return LivePulseFrame(float64(time.Now().UnixNano()) / float64(time.Second))
}

func (view *RoutingLiveView) HasActivePulse() bool {
	if view.collectorLiveStatus() == "running" &&
		view.CollectorDiscovery.Enabled &&
		view.CollectorDiscovery.ServiceKnown &&
		view.CollectorDiscovery.ServiceConnected &&
		view.CollectorDiscovery.EdgeAppsKnown &&
		view.CollectorDiscovery.EdgeApps > 0 {
		return true
	}
	if view.State.ServiceState() != "running" {
		return false
	}
	if HasDatabus(view.Config) && !view.State.DatabusDiscovery().Connected {
		return false
	}
	for _, row := range VisibleTopicRows(view.State.TopicRows()) {
		if row.Topic != "*" && (row.EdgeToCloud == "live" || row.CloudToEdge == "live") {
			return true
		}
	}
	return false
}

func (view *RoutingLiveView) PrintSnapshot(routingStatus string) RenderedView {
	if routingStatus != "" {
		view.State.serviceState = routingStatus
	}
	snapshot := view.Render(view.PulseFrame())
	view.LastSnapshot = snapshot
	return snapshot
}

func (view *RoutingLiveView) Render(pulseFrame int) RenderedView {
	if !HasDatabus(view.Config) {
		collectorStatus := view.collectorLiveStatus()
		header := GatewayLiveHeader(view.Config, "not configured", 0, pulseFrame)
		resource := gatewayLiveResourcesWithDiscovery(view.Config, collectorStatus, view.CollectorDiscovery, pulseFrame)
		if HasObservability(view.Config) {
			if view.CollectorSecure {
				resource.Warning = "secure"
			} else {
				resource.Warning = "not secure"
			}
		}
		return RenderedView{
			Title:           GatewayPanelTitle(),
			Header:          header,
			Resource:        resource,
			LogLines:        view.State.RecentLogs(),
			Border:          tui.RTIOrange,
			HideRoutes:      true,
			LogTitle:        "Collector Log",
			LogEmptyMessage: "Waiting for telemetry from Connext applications and gateways.",
			Findings:        view.Detector.Findings(),
		}
	}
	topicRows := VisibleTopicRows(view.State.TopicRows())
	activeRoutes := 0
	for _, row := range topicRows {
		if row.Topic != "*" && (row.EdgeToCloud == "live" || row.CloudToEdge == "live") {
			activeRoutes++
		}
	}
	status := view.State.ServiceState()
	if status == "running" && activeRoutes == 0 {
		status = "running, waiting for discovered topics"
	}
	collectorStatus := view.collectorLiveStatus()
	header := gatewayLiveHeaderWithDiscovery(view.Config, status, activeRoutes, pulseFrame, view.State.DatabusDiscovery())
	resource := gatewayLiveResourcesWithDiscovery(view.Config, collectorStatus, view.CollectorDiscovery, pulseFrame)
	if HasDatabus(view.Config) {
		if view.DatabusSecure {
			header.Warning = "secure"
		} else {
			header.Warning = "not secure"
		}
	}
	if HasObservability(view.Config) {
		if view.CollectorSecure {
			resource.Warning = "secure"
		} else {
			resource.Warning = "not secure"
		}
	}
	sortedRows := SortTopicRowsForDisplay(topicRows)
	routes := make([]RenderedRoute, 0, len(sortedRows))
	for _, row := range sortedRows {
		if len(routes) >= RoutingLiveRouteRows {
			break
		}
		routes = append(routes, RenderedRoute{
			IO:     RouteStatusChip(row, pulseFrame),
			Topic:  row.Topic,
			Type:   routeTypeLabel(row),
			Status: TopicStatusLabel(row),
		})
	}
	if len(topicRows) == 0 {
		routes = append(routes, RenderedRoute{IO: "[dim]○[/dim]", Topic: "waiting", Type: "No topics discovered yet", Status: "waiting"})
	}
	return RenderedView{
		Title:    GatewayPanelTitle(),
		Header:   header,
		Resource: resource,
		Routes:   routes,
		LogLines: view.State.RecentLogs(),
		LogTimes: view.State.RecentLogTimes(),
		Border:   tui.RTIOrange,
		Findings: view.Detector.Findings(),
	}
}

func routeTypeLabel(row TopicRouteRow) string {
	if row.TypeName != "" {
		return row.TypeName
	}
	if row.Topic == "*" {
		return "Any"
	}
	return "-"
}

func (view *RoutingLiveView) collectorLiveStatus() string {
	if !HasObservability(view.Config) {
		return "not configured"
	}
	if view.CollectorStatusFunc != nil {
		return view.CollectorStatusFunc(view.Config, view.CollectorName)
	}
	return view.CollectorStatus
}

func LivePulseFrame(now float64) int {
	return int(now/RoutingLivePulseInterval) % RoutingLivePulseFrames
}

func RouteStatusChip(row TopicRouteRow, pulseFrame int) string {
	edgeLive := row.EdgeToCloud == "live"
	cloudLive := row.CloudToEdge == "live"
	pulse := routeActivityPulse(pulseFrame)
	if edgeLive && cloudLive {
		return fmt.Sprintf("[green]%s[/green]", duplexRouteArrows(pulseFrame))
	}
	if edgeLive {
		return fmt.Sprintf("[green]↑%s[/green]", pulse)
	}
	if cloudLive {
		return fmt.Sprintf("[green]↓%s[/green]", pulse)
	}
	if row.EdgeToCloud == "starting" || row.CloudToEdge == "starting" {
		return fmt.Sprintf("[%s]◐[/]", tui.RTIBlue)
	}
	if row.EdgeToCloud == "stopping" || row.EdgeToCloud == "stopped" || row.CloudToEdge == "stopping" || row.CloudToEdge == "stopped" {
		return "[dim]◌[/dim]"
	}
	return fmt.Sprintf("[%s]○[/]", tui.RTIBlue)
}

func TopicStatusLabel(row TopicRouteRow) string {
	switch {
	case row.EdgeToCloud == "live" && row.CloudToEdge == "live":
		return "routing both"
	case row.EdgeToCloud == "live":
		return "routing upstream"
	case row.CloudToEdge == "live":
		return "routing downstream"
	}
	event := strings.TrimSpace(row.LastEvent)
	if event == "" || event == "waiting for topics" {
		return "waiting"
	}
	return event
}

func GatewayLiveHeader(config map[string]any, status string, routedTopicCount int, pulseFrame int) RenderedSummaryLine {
	return gatewaySummaryLine("databus", RoutingSummaryChip(status, routedTopicCount, pulseFrame), gatewayResourceTarget(config, "databus"))
}

func gatewayLiveHeaderWithDiscovery(config map[string]any, status string, routedTopicCount int, pulseFrame int, discovery DatabusDiscoveryState) RenderedSummaryLine {
	return gatewaySummaryLine("databus", databusDiscoverySummaryChip(status, routedTopicCount, pulseFrame, discovery), gatewayResourceTarget(config, "databus"))
}

func GatewayPanelTitle() string {
	return "[bold] Connext Cloud Gateway  [/bold]"
}

func GatewayLiveResources(config map[string]any, collectorStatus string) RenderedSummaryLine {
	return gatewaySummaryLine("observability", collectorSummaryChip(collectorStatus), gatewayResourceTarget(config, "observability"))
}

func gatewayLiveResourcesWithDiscovery(config map[string]any, collectorStatus string, discovery CollectorDiscoveryState, pulseFrame int) RenderedSummaryLine {
	return gatewaySummaryLine("observability", collectorDiscoverySummaryChip(collectorStatus, discovery, pulseFrame), gatewayResourceTarget(config, "observability"))
}

func gatewaySummaryLine(label string, status string, target string) RenderedSummaryLine {
	return RenderedSummaryLine{Label: label, Status: status, Target: target}
}

func gatewayResourceTarget(config map[string]any, resource string) string {
	templates, _ := config["templates"].(map[string]any)
	if resource == "databus" {
		return fmt.Sprintf("%s / %s", configString(config, "databus"), configString(templates, "gateway"))
	}
	return fmt.Sprintf("%s / %s", configString(config, "observability"), configString(templates, "collector"))
}

func configString(config map[string]any, key string) string {
	if config == nil {
		return "none"
	}
	if value, ok := config[key].(string); ok && value != "" {
		return value
	}
	return "none"
}

func shortGatewayStatus(status string) string {
	if strings.HasPrefix(status, "running, waiting") {
		return "waiting topics"
	}
	if status == "running" || status == "starting" {
		return status
	}
	return strings.ToLower(status)
}

func pulseGlyph(frame int) string {
	if frame%2 == 0 {
		return "●"
	}
	return "◉"
}

func routeActivityPulse(frame int) string {
	if frame%2 == 0 {
		return "·"
	}
	return "•"
}

func duplexRouteArrows(frame int) string {
	if frame%2 == 0 {
		return "↑↓"
	}
	return "↓↑"
}

func RoutingSummaryChip(status string, routedTopicCount int, pulseFrame int) string {
	shortStatus := shortGatewayStatus(status)
	if shortStatus == "not configured" {
		return "[dim]◌ not configured[/dim]"
	}
	if shortStatus == "stopped" {
		return "[dim]◌ stopped[/dim]"
	}
	if shortStatus == "waiting topics" {
		return fmt.Sprintf("[%s]○ waiting for topics[/]", tui.RTIBlue)
	}
	if shortStatus == "running" {
		noun := "topics"
		if routedTopicCount == 1 {
			noun = "topic"
		}
		return fmt.Sprintf("[green]%s routing %d %s[/green]", pulseGlyph(pulseFrame), routedTopicCount, noun)
	}
	if shortStatus == "starting" {
		return fmt.Sprintf("[%s]◐ starting[/]", tui.RTIBlue)
	}
	return fmt.Sprintf("[yellow]◌ %s[/yellow]", shortStatus)
}

func databusDiscoverySummaryChip(status string, routedTopicCount int, pulseFrame int, discovery DatabusDiscoveryState) string {
	running := status == "running" || strings.HasPrefix(status, "running, waiting")
	if !running {
		return RoutingSummaryChip(status, routedTopicCount, pulseFrame)
	}
	if !discovery.Known {
		return fmt.Sprintf("[%s]○ waiting for connection[/]", tui.RTIBlue)
	}
	if !discovery.Connected {
		return "[yellow]◌ disconnected[/yellow]"
	}
	if routedTopicCount == 0 {
		return "[green]● connected · waiting for topics[/green]"
	}
	noun := "topics"
	if routedTopicCount == 1 {
		noun = "topic"
	}
	return fmt.Sprintf("[green]%s connected · routing %d %s[/green]", pulseGlyph(pulseFrame), routedTopicCount, noun)
}

func collectorSummaryChip(status string) string {
	switch status {
	case "running":
		return "[green]● running[/green]"
	case "stopped":
		return "[dim]◌ stopped[/dim]"
	case "not configured":
		return "[dim]◌ not configured[/dim]"
	default:
		return fmt.Sprintf("[yellow]◌ %s[/yellow]", status)
	}
}

func collectorDiscoverySummaryChip(status string, discovery CollectorDiscoveryState, pulseFrame int) string {
	if status != "running" || !discovery.Enabled {
		return collectorSummaryChip(status)
	}
	if !discovery.ServiceKnown && !discovery.EdgeAppsKnown {
		return fmt.Sprintf("[%s]○ waiting for connections[/]", tui.RTIBlue)
	}
	edgeStatus := "waiting for edge apps"
	if discovery.EdgeAppsKnown && discovery.EdgeApps > 0 {
		noun := "apps"
		if discovery.EdgeApps == 1 {
			noun = "app"
		}
		edgeStatus = fmt.Sprintf("monitoring %d %s", discovery.EdgeApps, noun)
	}
	if discovery.ServiceKnown && discovery.ServiceConnected {
		glyph := "●"
		if discovery.EdgeAppsKnown && discovery.EdgeApps > 0 {
			glyph = pulseGlyph(pulseFrame)
		}
		return fmt.Sprintf("[green]%s connected · %s[/green]", glyph, edgeStatus)
	}
	if discovery.ServiceKnown {
		return fmt.Sprintf("[yellow]◌ disconnected · %s[/yellow]", edgeStatus)
	}
	if !discovery.EdgeAppsKnown || discovery.EdgeApps == 0 {
		return fmt.Sprintf("[%s]○ waiting for connections[/]", tui.RTIBlue)
	}
	return fmt.Sprintf("[%s]○ waiting · %s[/]", tui.RTIBlue, edgeStatus)
}

func (renderer *TerminalRenderer) Render(view RenderedView) error {
	if renderer == nil || renderer.Out == nil {
		return nil
	}
	if renderer.screen == nil {
		renderer.screen = tui.NewScreen(renderer.Out)
	}
	width, height := tui.TerminalSize(renderer.Out, defaultTerminalWidth, defaultTerminalHeight)
	return renderer.screen.Paint(renderFrameLines(view, width, height), width, height)
}

// Finish restores the cursor once the live view is done so follow-up messages
// print below the last frame.
func (renderer *TerminalRenderer) Finish() error {
	if renderer == nil || renderer.screen == nil {
		return nil
	}
	return renderer.screen.Finish()
}

func renderANSI(view RenderedView) string {
	return renderANSIForSize(view, defaultTerminalWidth, defaultTerminalHeight)
}

func renderANSIForSize(view RenderedView, width int, height int) string {
	return strings.Join(renderFrameLines(view, width, height), "\n")
}

func renderFrameLines(view RenderedView, width int, height int) []string {
	if width <= 0 {
		width = defaultTerminalWidth
	}
	if height <= 0 {
		height = defaultTerminalHeight
	}
	contentWidth := tui.MaxInt(24, width-4)
	topicWidth, typeWidth, statusWidth := resolveRouteTableWidths(view.Routes, contentWidth)
	logTitle := "Routing Log"
	if strings.TrimSpace(view.LogTitle) != "" {
		logTitle = view.LogTitle
	}
	logEmptyMessage := "Waiting for route activity..."
	if strings.TrimSpace(view.LogEmptyMessage) != "" {
		logEmptyMessage = view.LogEmptyMessage
	}
	routeLines := []string{
		formatRouteHeaderLine(topicWidth, typeWidth, statusWidth),
	}
	if len(view.Routes) == 0 {
		routeLines = append(routeLines, formatRouteEmptyLine(contentWidth))
	} else {
		for _, route := range view.Routes {
			routeLines = append(routeLines, formatRouteLine(route, topicWidth, typeWidth, statusWidth))
		}
	}
	logLines := []string{}
	logEntries := gatewayLogEntries(view.LogLines, view.LogTimes)
	if len(logEntries) == 0 {
		logLines = append(logLines, formatLogLine(logEmptyMessage, contentWidth))
	} else {
		for _, entry := range tui.CompactLogEntries(logEntries) {
			logLines = append(logLines, tui.FormatLogEntry(entry, contentWidth))
		}
	}
	summaryLines := []RenderedSummaryLine{view.Header, view.Resource}
	summaryContentWidth := tui.MaxInt(8, width-4)
	summaryLayout := resolveSummaryLayout(summaryLines, summaryContentWidth)
	summaryPanel := tui.RenderPanel(tui.StripMarkup(view.Title), []string{
		formatSummaryPanelLine(view.Header, summaryLayout),
		formatSummaryPanelLine(view.Resource, summaryLayout),
	}, width, summaryPanelTheme())
	diagnosticsPanel := tui.RenderDiagnosticsPanel(view.Findings, width)
	if view.HideRoutes {
		fixedOverhead := 1 + len(summaryPanel) + 1 + 2
		if len(diagnosticsPanel) > 0 {
			fixedOverhead += len(diagnosticsPanel) + 1
		}
		logBudget := height - fixedOverhead
		if logBudget <= 0 {
			logBudget = 1
		}
		logsPanel := tui.RenderPanel(logTitle, resizePanelBody(logLines, logBudget, contentWidth), width, logPanelTheme())
		lines := []string{""}
		lines = append(lines, summaryPanel...)
		lines = append(lines, "")
		if len(diagnosticsPanel) > 0 {
			lines = append(lines, diagnosticsPanel...)
			lines = append(lines, "")
		}
		lines = append(lines, logsPanel...)
		return lines
	}
	fixedOverhead := 1 + len(summaryPanel) + 1 + 2 + 1 + 2
	if len(diagnosticsPanel) > 0 {
		fixedOverhead += len(diagnosticsPanel) + 1
	}
	available := height - fixedOverhead
	routeBudget, logBudget := tui.SplitSectionBudget(available, len(routeLines), len(logLines))
	if routeBudget <= 0 {
		routeBudget = 1
	}
	if logBudget <= 0 {
		logBudget = 1
	}
	routesPanel := tui.RenderPanel("Routes", resizePanelBody(routeLines, routeBudget, contentWidth), width, routesPanelTheme())
	logsPanel := tui.RenderPanel(logTitle, resizePanelBody(logLines, logBudget, contentWidth), width, logPanelTheme())
	lines := []string{""}
	lines = append(lines, summaryPanel...)
	lines = append(lines, "")
	lines = append(lines, routesPanel...)
	lines = append(lines, "")
	if len(diagnosticsPanel) > 0 {
		lines = append(lines, diagnosticsPanel...)
		lines = append(lines, "")
	}
	lines = append(lines, logsPanel...)
	return lines
}

// gatewayLogEntries pairs each summarized routing log line with its severity and
// (when known) arrival time so the shared renderer can dedup, color, and stamp.
func gatewayLogEntries(lines []string, times []time.Time) []tui.LogEntry {
	entries := make([]tui.LogEntry, len(lines))
	for i, line := range lines {
		entry := tui.LogEntry{Text: line, Severity: gatewayLogSeverity(line)}
		if i < len(times) {
			entry.Time = times[i]
		}
		entries[i] = entry
	}
	return entries
}

func resizePanelBody(lines []string, size int, width int) []string {
	if size <= 0 {
		return nil
	}
	body := make([]string, 0, size)
	if len(lines) >= size {
		return append(body, lines[:size]...)
	}
	body = append(body, lines...)
	for len(body) < size {
		body = append(body, tui.Dim(strings.Repeat(" ", tui.MaxInt(1, width-4))))
	}
	return body
}

type summaryColumns struct {
	labelWidth   int
	statusWidth  int
	targetWidth  int
	showWarnings bool
}

func resolveSummaryLayout(lines []RenderedSummaryLine, contentWidth int) summaryColumns {
	maxWarningWidth := 0
	maxTargetWidth := 0
	for _, line := range lines {
		if targetWidth := tui.DisplayWidth(line.Target); targetWidth > maxTargetWidth {
			maxTargetWidth = targetWidth
		}
		if line.Warning != "" {
			warningWidth := tui.DisplayWidth(tui.StyleInlineWarning(line.Warning)) + 2
			if warningWidth > maxWarningWidth {
				maxWarningWidth = warningWidth
			}
		}
	}
	fullWidth := summaryLabelMaxWidth + 2 + summaryStatusMaxWidth
	if maxTargetWidth > 0 {
		fullWidth += 2 + maxTargetWidth
	}
	showWarnings := maxWarningWidth > 0 && contentWidth >= fullWidth+maxWarningWidth
	if showWarnings {
		return summaryColumns{summaryLabelMaxWidth, summaryStatusMaxWidth, maxTargetWidth, true}
	}
	labelWidth := contentWidth - fullWidth + summaryLabelMaxWidth
	if labelWidth >= summaryLabelMaxWidth {
		return summaryColumns{summaryLabelMaxWidth, summaryStatusMaxWidth, maxTargetWidth, false}
	}
	labelWidth = summaryLabelMinWidth
	remaining := contentWidth - labelWidth - 2
	if remaining < summaryStatusMaxWidth {
		return summaryColumns{labelWidth, tui.MaxInt(summaryStatusMinWidth, remaining), 0, false}
	}
	targetWidth := 0
	if targetSpace := remaining - summaryStatusMaxWidth; targetSpace > 2 {
		targetWidth = tui.MinInt(maxTargetWidth, targetSpace-2)
	}
	return summaryColumns{labelWidth, summaryStatusMaxWidth, targetWidth, false}
}

func formatSummaryPanelLine(line RenderedSummaryLine, layout summaryColumns) string {
	labelText := strings.ToUpper(line.Label)
	if layout.labelWidth == summaryLabelMinWidth {
		switch strings.ToLower(line.Label) {
		case "databus":
			labelText = "DATA"
		case "observability":
			labelText = "OBS"
		}
	}
	label := tui.StyleLabel(labelText, layout.labelWidth)
	warning := ""
	if layout.showWarnings && line.Warning != "" {
		warning = tui.StyleInlineWarning(line.Warning)
	}
	status := tui.StyleChipWidth(line.Status, layout.statusWidth)
	formatted := fmt.Sprintf("%s  %s", label, status)
	if layout.targetWidth > 0 {
		target := tui.StyleTarget(tui.TruncateDisplay(line.Target, layout.targetWidth), layout.targetWidth)
		formatted += "  " + target
	}
	if warning != "" {
		formatted += "  " + warning
	}
	return formatted
}
func resolveRouteTableWidths(routes []RenderedRoute, contentWidth int) (int, int, int) {
	topicWidth := boundedColumnWidth(routes, func(route RenderedRoute) string { return route.Topic }, "Topic", routeTopicMinWidth, routeTopicMaxWidth)
	typeWidth := boundedColumnWidth(routes, func(route RenderedRoute) string { return route.Type }, "Type", routeTypeMinWidth, routeTypeMaxWidth)
	statusWidth := contentWidth - routeIOWidth - topicWidth - typeWidth - 6
	for statusWidth < 12 && topicWidth > routeTopicMinWidth {
		topicWidth--
		statusWidth++
	}
	for statusWidth < 12 && typeWidth > routeTypeMinWidth {
		typeWidth--
		statusWidth++
	}
	return topicWidth, typeWidth, tui.MaxInt(12, statusWidth)
}

func formatRouteHeaderLine(topicWidth int, typeWidth int, statusWidth int) string {
	return fmt.Sprintf("%s  %s  %s  %s",
		tui.StyleColumnHeader("IO", routeIOWidth),
		tui.StyleColumnHeader("Topic", topicWidth),
		tui.StyleColumnHeader("Type", typeWidth),
		tui.StyleColumnHeader("Status", statusWidth))
}

func formatRouteEmptyLine(contentWidth int) string {
	return tui.Dim(tui.PadDisplay("No topics discovered yet", tui.MaxInt(1, contentWidth)))
}

func formatRouteLine(route RenderedRoute, topicWidth int, typeWidth int, statusWidth int) string {
	status := tui.TruncateDisplay(route.Status, statusWidth)
	return fmt.Sprintf("%s  %s  %s  %s",
		tui.StyleChipWidth(route.IO, routeIOWidth),
		tui.StyleBold(tui.TruncateDisplay(route.Topic, topicWidth), topicWidth),
		styleRouteType(tui.TruncateDisplay(route.Type, typeWidth), typeWidth),
		styleRouteStatus(status, statusWidth))
}

// formatLogLine classifies one Routing Log line by keyword and renders it via
// the shared tui helper, so the glyph/color for a given severity stays in sync
// with the Edge-Sync Agent log panel.
func formatLogLine(line string, contentWidth int) string {
	return tui.FormatLogLine(line, contentWidth, gatewayLogSeverity(line))
}

// gatewayLogSeverity maps a summarized routing log line to its display severity.
func gatewayLogSeverity(line string) tui.LogSeverity {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(lower, "warning"):
		return tui.LogInfo
	case strings.Contains(lower, "error") || strings.Contains(lower, "mismatch") || strings.Contains(lower, "missing"):
		return tui.LogWarn
	case strings.HasPrefix(lower, "input lost") || strings.HasPrefix(lower, "output lost") || strings.HasSuffix(lower, " lost"):
		return tui.LogWarn
	case strings.HasPrefix(lower, "run ") || strings.HasPrefix(lower, "start ") || strings.HasPrefix(lower, "routing "):
		return tui.LogGood
	case strings.HasPrefix(lower, "discovered ") || strings.HasPrefix(lower, "disposed ") || strings.Contains(lower, "matched"):
		return tui.LogInfo
	default:
		return tui.LogInfo
	}
}
func summaryPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleTitle, BorderStyle: tui.StyleOrangeBorder, PaddedBody: true}
}

func routesPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleSection, BorderStyle: tui.StyleBlueBorder}
}

func logPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleMutedSection, BorderStyle: tui.StyleGrayBorder}
}

func styleRouteType(value string, width int) string {
	return tui.PadDisplay(value, width)
}

func styleRouteStatus(value string, width int) string {
	content := tui.PadDisplay(value, width)
	lower := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.HasPrefix(lower, "routing both"):
		return "\x1b[1;32m" + content + "\x1b[0m"
	case strings.HasPrefix(lower, "routing upstream") || strings.HasPrefix(lower, "routing downstream") || strings.HasPrefix(lower, "routing"):
		return "\x1b[32m" + content + "\x1b[0m"
	case strings.HasSuffix(lower, " found"):
		return "\x1b[36m" + content + "\x1b[0m"
	case strings.HasSuffix(lower, " lost") || strings.Contains(lower, "mismatch") || strings.Contains(lower, "missing"):
		return "\x1b[33m" + content + "\x1b[0m"
	case strings.Contains(lower, "waiting") || strings.Contains(lower, "listening") || strings.Contains(lower, "starting"):
		return tui.Dim(content)
	default:
		return tui.Dim(content)
	}
}
func boundedColumnWidth(routes []RenderedRoute, accessor func(RenderedRoute) string, header string, minWidth int, maxWidth int) int {
	width := tui.DisplayWidth(header)
	for _, route := range routes {
		if candidate := tui.DisplayWidth(accessor(route)); candidate > width {
			width = candidate
		}
	}
	if width < minWidth {
		width = minWidth
	}
	if width > maxWidth {
		width = maxWidth
	}
	return width
}

func RenderSetupIntro(databusCount int, observabilityCount int, cursorSelection bool) string {
	hint := "Use arrow keys to choose and Enter to confirm."
	if !cursorSelection {
		hint = "Type the option number and press Enter to continue."
	}
	rows := []string{
		"Connext Cloud Gateway setup",
		"Create a project-local gateway configuration for this workspace.",
		"",
		fmt.Sprintf("Databuses available: %d", databusCount),
		fmt.Sprintf("Observability services: %d", observabilityCount),
		"",
		hint,
	}
	width := 0
	for _, row := range rows {
		if rowWidth := tui.DisplayWidth(row); rowWidth > width {
			width = rowWidth
		}
	}
	top := "╭" + strings.Repeat("─", width+2) + "╮"
	bottom := "╰" + strings.Repeat("─", width+2) + "╯"
	lines := []string{"", "\x1b[38;5;110m" + top + "\x1b[0m"}
	for index, row := range rows {
		content := tui.PadDisplay(row, width)
		switch index {
		case 0:
			content = tui.StyleTitle(content)
		case 1, 6:
			content = tui.Dim(content)
		}
		lines = append(lines, fmt.Sprintf("\x1b[38;5;110m│\x1b[0m %s \x1b[38;5;110m│\x1b[0m", content))
	}
	lines = append(lines, "\x1b[38;5;110m"+bottom+"\x1b[0m", "")
	return strings.Join(lines, "\n")
}

func RenderWarningMessage(message string) string {
	return fmt.Sprintf("\x1b[33m⚠\x1b[0m %s\n", message)
}

func RenderInfoMessage(message string) string {
	return fmt.Sprintf("\x1b[38;5;110m•\x1b[0m %s\n", message)
}

func RenderSuccessMessage(message string) string {
	return fmt.Sprintf("\x1b[32m✓\x1b[0m %s\n", message)
}

func RenderKeyValuePanel(title string, rows []KeyValueRow) string {
	content := make([]string, 0, len(rows)+1)
	content = append(content, tui.StyleSection(title))
	if len(rows) == 1 && strings.Contains(rows[0].Value, "://") {
		if rows[0].Key != "" {
			content = append(content, tui.Dim(rows[0].Key+":"))
		}
		content = append(content, tui.StyleLink(rows[0].Value))
	} else {
		for _, row := range rows {
			if row.Key == "" {
				content = append(content, row.Value)
				continue
			}
			content = append(content, fmt.Sprintf("%s %s", tui.Dim(row.Key+":"), row.Value))
		}
	}
	width := 0
	for _, row := range content {
		if rowWidth := tui.DisplayWidth(row); rowWidth > width {
			width = rowWidth
		}
	}
	top := "╭" + strings.Repeat("─", width+2) + "╮"
	bottom := "╰" + strings.Repeat("─", width+2) + "╯"
	lines := []string{"\x1b[38;5;110m" + top + "\x1b[0m"}
	for _, row := range content {
		lines = append(lines, fmt.Sprintf("\x1b[38;5;110m│\x1b[0m %s \x1b[38;5;110m│\x1b[0m", tui.PadStyled(row, width)))
	}
	lines = append(lines, "\x1b[38;5;110m"+bottom+"\x1b[0m", "")
	return strings.Join(lines, "\n")
}
