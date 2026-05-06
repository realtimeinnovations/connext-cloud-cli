package gateway

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	SelectedOptionOrange  = "#FF9D00"
	RTIOrange             = SelectedOptionOrange
	RTIBlue               = "#5f819d"
	RoutingLiveRouteRows  = 16
	defaultTerminalWidth  = 120
	defaultTerminalHeight = 40
	summaryLabelWidth     = 14
	summaryStatusWidth    = 24
	routeIOWidth          = 4
	routeTopicMinWidth    = 20
	routeTopicMaxWidth    = 28
	routeTypeMinWidth     = 12
	routeTypeMaxWidth     = 18
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

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
	Title    string
	Header   RenderedSummaryLine
	Resource RenderedSummaryLine
	Routes   []RenderedRoute
	LogLines []string
	Border   string
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
	State               *RoutingState
	CollectorStatus     string
	CollectorStatusFunc func(config map[string]any, collectorName string) string
	LastSnapshot        RenderedView
	Enabled             bool
}

type TerminalRenderer struct {
	Out io.Writer
}

type panelTheme struct {
	titleStyle  func(string) string
	borderStyle func(string) string
	paddedBody  bool
}

func NewRoutingLiveView(config map[string]any) *RoutingLiveView {
	return &RoutingLiveView{
		Config:          config,
		State:           NewRoutingState(RoutingLiveLogLines),
		CollectorStatus: "not configured",
		Enabled:         true,
	}
}

func (view *RoutingLiveView) HandleLine(line string) {
	view.State.Update(line)
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
	if view.State.ServiceState() != "running" {
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
	header := GatewayLiveHeader(view.Config, status, activeRoutes, pulseFrame)
	resource := GatewayLiveResources(view.Config, collectorStatus)
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
		LogLines: append([]string(nil), view.State.RecentLogs()...),
		Border:   RTIOrange,
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
	if !hasObservability(view.Config) {
		return "not configured"
	}
	if view.CollectorStatusFunc != nil {
		return view.CollectorStatusFunc(view.Config, view.CollectorName)
	}
	return view.CollectorStatus
}

func hasObservability(config map[string]any) bool {
	observability, ok := config["observability"].(string)
	if !ok || observability == "" {
		return false
	}
	templates, _ := config["templates"].(map[string]any)
	collector, _ := templates["collector"].(string)
	return collector != ""
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
		return fmt.Sprintf("[%s]◐[/]", RTIBlue)
	}
	if row.EdgeToCloud == "stopping" || row.EdgeToCloud == "stopped" || row.CloudToEdge == "stopping" || row.CloudToEdge == "stopped" {
		return "[dim]◌[/dim]"
	}
	return fmt.Sprintf("[%s]○[/]", RTIBlue)
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
	return gatewaySummaryLine("data", RoutingSummaryChip(status, routedTopicCount, pulseFrame), gatewayResourceTarget(config, "databus"))
}

func GatewayPanelTitle() string {
	return "[bold] Connext Cloud Gateway  [/bold]"
}

func GatewayLiveResources(config map[string]any, collectorStatus string) RenderedSummaryLine {
	return gatewaySummaryLine("observability", collectorSummaryChip(collectorStatus), gatewayResourceTarget(config, "observability"))
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
	if shortStatus == "waiting topics" {
		return fmt.Sprintf("[%s]○ waiting topics[/]", RTIBlue)
	}
	if shortStatus == "running" {
		noun := "topics"
		if routedTopicCount == 1 {
			noun = "topic"
		}
		return fmt.Sprintf("[green]%s routing %d %s[/green]", pulseGlyph(pulseFrame), routedTopicCount, noun)
	}
	if shortStatus == "starting" {
		return fmt.Sprintf("[%s]◐ starting[/]", RTIBlue)
	}
	return fmt.Sprintf("[yellow]◌ %s[/yellow]", shortStatus)
}

func collectorSummaryChip(status string) string {
	switch status {
	case "running":
		return "[green]● running[/green]"
	case "stopped":
		return "[dim]◌ stopped[/dim]"
	case "not configured":
		return "[dim]◌ not configured[/dim]"
	case "docker unavailable":
		return "[yellow]◌ docker unavailable[/yellow]"
	default:
		return fmt.Sprintf("[yellow]◌ %s[/yellow]", status)
	}
}

func (renderer TerminalRenderer) Render(view RenderedView) error {
	if renderer.Out == nil {
		return nil
	}
	width, height := terminalSize(renderer.Out)
	_, err := io.WriteString(renderer.Out, renderANSIForSize(view, width, height))
	return err
}

func renderANSI(view RenderedView) string {
	return renderANSIForSize(view, defaultTerminalWidth, defaultTerminalHeight)
}

func renderANSIForSize(view RenderedView, width int, height int) string {
	if width <= 0 {
		width = defaultTerminalWidth
	}
	if height <= 0 {
		height = defaultTerminalHeight
	}
	contentWidth := maxInt(24, width-4)
	topicWidth, typeWidth, statusWidth := resolveRouteTableWidths(view.Routes, contentWidth)
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
	if len(view.LogLines) == 0 {
		logLines = append(logLines, formatLogLine("Waiting for route activity...", contentWidth))
	} else {
		for _, line := range compactLogLines(view.LogLines) {
			logLines = append(logLines, formatLogLine(line, contentWidth))
		}
	}
	summaryPanel := renderPanel(stripMarkup(view.Title), []string{
		formatSummaryPanelLine(view.Header, contentWidth),
		formatSummaryPanelLine(view.Resource, contentWidth),
	}, width, summaryPanelTheme())
	fixedOverhead := 1 + len(summaryPanel) + 1 + 2 + 1 + 2
	available := height - fixedOverhead
	routeBudget, logBudget := splitSectionBudget(available, len(routeLines), len(logLines))
	if routeBudget <= 0 {
		routeBudget = 1
	}
	if logBudget <= 0 {
		logBudget = 1
	}
	routesPanel := renderPanel("Routes", resizePanelBody(routeLines, routeBudget, contentWidth), width, routesPanelTheme())
	logsPanel := renderPanel("Routing Log", resizePanelBody(logLines, logBudget, contentWidth), width, logPanelTheme())
	lines := []string{"\x1b[H\x1b[J"}
	lines = append(lines, summaryPanel...)
	lines = append(lines, "")
	lines = append(lines, routesPanel...)
	lines = append(lines, "")
	lines = append(lines, logsPanel...)
	return strings.Join(lines, "\n")
}

func splitSectionBudget(available int, routeLines int, logLines int) (int, int) {
	if available <= 0 {
		return 0, 0
	}
	if routeLines == 0 {
		routeLines = 1
	}
	if logLines == 0 {
		logLines = 1
	}
	minLogLines := minInt(logLines, 4)
	routeBudget := minInt(routeLines, maxInt(1, available-minLogLines))
	logBudget := minInt(logLines, maxInt(1, available-routeBudget))
	for routeBudget+logBudget > available && logBudget > 1 {
		logBudget--
	}
	for routeBudget+logBudget > available && routeBudget > 1 {
		routeBudget--
	}
	if routeBudget+logBudget > available {
		logBudget = maxInt(0, available-routeBudget)
	}
	return routeBudget, logBudget
}

func formatSummaryLine(line RenderedSummaryLine) string {
	formatted := fmt.Sprintf("  %s %s %s", dim(padDisplay(line.Label, summaryLabelWidth)), styleChipWidth(line.Status, summaryStatusWidth), line.Target)
	if line.Warning != "" {
		formatted += "  " + styleInlineWarning(line.Warning)
	}
	return formatted
}

func renderPanel(title string, body []string, width int, theme panelTheme) []string {
	if width < 12 {
		width = 12
	}
	lines := []string{panelTopBorder(title, width, theme)}
	if theme.paddedBody {
		lines = append(lines, panelBodyLine("", width, theme))
	}
	for _, line := range body {
		lines = append(lines, panelBodyLine(line, width, theme))
	}
	if theme.paddedBody {
		lines = append(lines, panelBodyLine("", width, theme))
	}
	lines = append(lines, panelBottomBorder(width, theme))
	return lines
}

func panelTopBorder(title string, width int, theme panelTheme) string {
	innerWidth := maxInt(1, width-2)
	label := truncateDisplay(title, maxInt(1, innerWidth-3))
	filler := maxInt(0, innerWidth-displayWidth(label)-3)
	styled := theme.titleStyle(label)
	left := theme.borderStyle("┌─ ")
	middle := theme.borderStyle(" " + strings.Repeat("─", filler))
	right := theme.borderStyle("┐")
	return left + styled + middle + right
}

func panelBottomBorder(width int, theme panelTheme) string {
	return theme.borderStyle("└" + strings.Repeat("─", maxInt(1, width-2)) + "┘")
}

func panelBodyLine(content string, width int, theme panelTheme) string {
	innerWidth := maxInt(1, width-4)
	if displayWidth(content) > innerWidth {
		content = truncateDisplay(stripANSIEscapes(content), innerWidth)
	}
	return theme.borderStyle("│ ") + padStyled(content, innerWidth) + theme.borderStyle(" │")
}

func padStyled(value string, width int) string {
	visible := displayWidth(value)
	if visible >= width {
		return value
	}
	return value + strings.Repeat(" ", width-visible)
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
		body = append(body, dim(strings.Repeat(" ", maxInt(1, width-4))))
	}
	return body
}

func formatSummaryPanelLine(line RenderedSummaryLine, contentWidth int) string {
	label := styleSummaryLabel(strings.ToUpper(line.Label), summaryLabelWidth)
	warningWidth := 0
	warning := ""
	if line.Warning != "" {
		warning = styleInlineWarning(line.Warning)
		warningWidth = displayWidth(warning) + 2
	}
	targetWidth := maxInt(8, contentWidth-summaryLabelWidth-summaryStatusWidth-warningWidth-4)
	status := styleChipWidth(line.Status, summaryStatusWidth)
	target := styleSummaryTarget(truncateDisplay(line.Target, targetWidth), targetWidth)
	formatted := fmt.Sprintf("%s  %s  %s", label, status, target)
	if warning != "" {
		formatted += "  " + warning
	}
	return formatted
}

func styleSummaryLabel(value string, width int) string {
	return "\x1b[2;38;5;110m" + padDisplay(value, width) + "\x1b[0m"
}

func styleSummaryTarget(value string, width int) string {
	return "\x1b[1m" + padDisplay(value, width) + "\x1b[0m"
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
	return topicWidth, typeWidth, maxInt(12, statusWidth)
}

func formatRouteHeaderLine(topicWidth int, typeWidth int, statusWidth int) string {
	return fmt.Sprintf("%s  %s  %s  %s",
		dim(padDisplay("IO", routeIOWidth)),
		dim(padDisplay("Topic", topicWidth)),
		dim(padDisplay("Type", typeWidth)),
		dim(padDisplay("Status", statusWidth)))
}

func formatRouteEmptyLine(contentWidth int) string {
	return dim(padDisplay("No topics discovered yet", maxInt(1, contentWidth)))
}

func formatRouteLine(route RenderedRoute, topicWidth int, typeWidth int, statusWidth int) string {
	status := truncateDisplay(route.Status, statusWidth)
	return fmt.Sprintf("%s  %s  %s  %s",
		styleChipWidth(route.IO, routeIOWidth),
		styleRouteTopic(truncateDisplay(route.Topic, topicWidth), topicWidth),
		styleRouteType(truncateDisplay(route.Type, typeWidth), typeWidth),
		styleRouteStatus(status, statusWidth))
}

func compactLogLines(lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	compacted := make([]string, 0, len(lines))
	current := lines[0]
	count := 1
	flush := func() {
		if count > 1 {
			compacted = append(compacted, fmt.Sprintf("%s (x%d)", current, count))
		} else {
			compacted = append(compacted, current)
		}
	}
	for _, line := range lines[1:] {
		if line == current {
			count++
			continue
		}
		flush()
		current = line
		count = 1
	}
	flush()
	return compacted
}

func formatLogLine(line string, contentWidth int) string {
	trimmed := strings.TrimSpace(line)
	textWidth := maxInt(8, contentWidth-2)
	formatted := truncateDisplay(trimmed, textWidth)
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "warning"):
		return "· " + dim(formatted)
	case strings.Contains(lower, "error") || strings.Contains(lower, "mismatch") || strings.Contains(lower, "missing"):
		return "! " + "\x1b[33m" + formatted + "\x1b[0m"
	case strings.HasPrefix(lower, "input lost") || strings.HasPrefix(lower, "output lost") || strings.HasSuffix(lower, " lost"):
		return "! " + "\x1b[33m" + formatted + "\x1b[0m"
	case strings.HasPrefix(lower, "run ") || strings.HasPrefix(lower, "start ") || strings.HasPrefix(lower, "routing "):
		return "• " + "\x1b[32m" + formatted + "\x1b[0m"
	case strings.HasPrefix(lower, "discovered ") || strings.HasPrefix(lower, "disposed ") || strings.Contains(lower, "matched"):
		return "· " + dim(formatted)
	default:
		return "· " + dim(formatted)
	}
}

func styleTitle(value string) string {
	return "\x1b[1;38;5;208m" + value + "\x1b[0m"
}

func styleSection(value string) string {
	return "\x1b[1;38;5;110m" + value + "\x1b[0m"
}

func styleMutedSection(value string) string {
	return "\x1b[1;38;5;245m" + value + "\x1b[0m"
}

func styleOrangeBorder(value string) string {
	return "\x1b[38;5;208m" + value + "\x1b[0m"
}

func styleBlueBorder(value string) string {
	return "\x1b[38;5;110m" + value + "\x1b[0m"
}

func styleGrayBorder(value string) string {
	return "\x1b[38;5;245m" + value + "\x1b[0m"
}

func summaryPanelTheme() panelTheme {
	return panelTheme{titleStyle: styleTitle, borderStyle: styleOrangeBorder, paddedBody: true}
}

func routesPanelTheme() panelTheme {
	return panelTheme{titleStyle: styleSection, borderStyle: styleBlueBorder}
}

func logPanelTheme() panelTheme {
	return panelTheme{titleStyle: styleMutedSection, borderStyle: styleGrayBorder}
}

func dim(value string) string {
	return "\x1b[2m" + value + "\x1b[0m"
}

func styleChip(value string) string {
	return styleChipText(value, stripMarkup(value))
}

func styleInlineWarning(value string) string {
	if value == "secure" {
		return "\x1b[38;2;95;129;157m(• " + value + ")\x1b[0m"
	}
	return "\x1b[33m(⚠ " + value + ")\x1b[0m"
}

func styleRouteTopic(value string, width int) string {
	return "\x1b[1m" + padDisplay(value, width) + "\x1b[0m"
}

func styleRouteType(value string, width int) string {
	return padDisplay(value, width)
}

func styleRouteStatus(value string, width int) string {
	content := padDisplay(value, width)
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
		return dim(content)
	default:
		return dim(content)
	}
}

func styleChipWidth(value string, width int) string {
	return styleChipText(value, padDisplay(stripMarkup(value), width))
}

func styleChipText(markup string, content string) string {
	switch {
	case strings.Contains(markup, "[green]"):
		return "\x1b[32m" + content + "\x1b[0m"
	case strings.Contains(markup, "[yellow]"):
		return "\x1b[33m" + content + "\x1b[0m"
	case strings.Contains(markup, "[dim]"):
		return dim(content)
	default:
		return "\x1b[36m" + content + "\x1b[0m"
	}
}

func boundedColumnWidth(routes []RenderedRoute, accessor func(RenderedRoute) string, header string, minWidth int, maxWidth int) int {
	width := displayWidth(header)
	for _, route := range routes {
		if candidate := displayWidth(accessor(route)); candidate > width {
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

func padDisplay(value string, width int) string {
	clean := stripANSIEscapes(value)
	runes := []rune(clean)
	if len(runes) > width {
		return truncateDisplay(clean, width)
	}
	return clean + strings.Repeat(" ", width-len(runes))
}

func truncateDisplay(value string, width int) string {
	if width <= 0 {
		return ""
	}
	clean := stripANSIEscapes(value)
	runes := []rune(clean)
	if len(runes) <= width {
		return clean
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
}

func displayWidth(value string) int {
	return len([]rune(stripANSIEscapes(value)))
}

func stripANSIEscapes(value string) string {
	return ansiEscapePattern.ReplaceAllString(stripMarkup(value), "")
}

func terminalSize(out io.Writer) (int, int) {
	file, ok := out.(*os.File)
	if !ok {
		return defaultTerminalWidth, defaultTerminalHeight
	}
	width, height, err := term.GetSize(int(file.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return defaultTerminalWidth, defaultTerminalHeight
	}
	return width, height
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
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
		if rowWidth := displayWidth(row); rowWidth > width {
			width = rowWidth
		}
	}
	top := "╭" + strings.Repeat("─", width+2) + "╮"
	bottom := "╰" + strings.Repeat("─", width+2) + "╯"
	lines := []string{"", "\x1b[38;5;110m" + top + "\x1b[0m"}
	for index, row := range rows {
		content := padDisplay(row, width)
		switch index {
		case 0:
			content = styleTitle(content)
		case 1, 6:
			content = dim(content)
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
	content = append(content, styleSection(title))
	if len(rows) == 1 && strings.Contains(rows[0].Value, "://") {
		if rows[0].Key != "" {
			content = append(content, dim(rows[0].Key+":"))
		}
		content = append(content, rows[0].Value)
	} else {
		for _, row := range rows {
			content = append(content, fmt.Sprintf("%s %s", dim(row.Key+":"), row.Value))
		}
	}
	width := 0
	for _, row := range content {
		if rowWidth := displayWidth(row); rowWidth > width {
			width = rowWidth
		}
	}
	top := "╭" + strings.Repeat("─", width+2) + "╮"
	bottom := "╰" + strings.Repeat("─", width+2) + "╯"
	lines := []string{"\x1b[38;5;110m" + top + "\x1b[0m"}
	for _, row := range content {
		lines = append(lines, fmt.Sprintf("\x1b[38;5;110m│\x1b[0m %s \x1b[38;5;110m│\x1b[0m", padDisplay(row, width)))
	}
	lines = append(lines, "\x1b[38;5;110m"+bottom+"\x1b[0m", "")
	return strings.Join(lines, "\n")
}

func stripMarkup(value string) string {
	replacer := strings.NewReplacer(
		"[green]", "",
		"[/green]", "",
		"[yellow]", "",
		"[/yellow]", "",
		"[dim]", "",
		"[/dim]", "",
		"[bold]", "",
		"[/bold]", "",
		"[/]", "",
		"[#5f819d]", "",
	)
	return replacer.Replace(value)
}
