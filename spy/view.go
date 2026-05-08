package spy

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	SpyOrange             = "#FF9D00"
	spyDefaultWidth       = 120
	spyDefaultHeight      = 40
	spySummaryLabelWidth  = 12
	spySummaryStatusWidth = 24
)

type SummaryLine struct {
	Label   string
	Status  string
	Target  string
	Warning string
}

type RenderedTopic struct {
	Activity string
	Topic    string
	Type     string
	Writers  int
	Readers  int
	Samples  int
	Status   string
}

type RenderedSample struct {
	Topic  string
	Sample string
}

type RenderedStats struct {
	DataWriters int
	DataReaders int
	Rows        []TopicRow
}

type RenderedView struct {
	Title   string
	Header  SummaryLine
	Topics  []RenderedTopic
	Samples []RenderedSample
	Stats   RenderedStats
	Border  string
}

type LiveView struct {
	Config        map[string]any
	State         *SpyState
	DatabusSecure bool
	LastSnapshot  RenderedView
}

type TerminalRenderer struct {
	Out io.Writer
}

type panelTheme struct {
	titleStyle  func(string) string
	borderStyle func(string) string
	paddedBody  bool
}

func NewLiveView(config map[string]any) *LiveView {
	return &LiveView{Config: config, State: NewSpyState(SpyLiveLogLines)}
}

func (view *LiveView) HandleLine(line string) {
	view.State.Update(line)
}

func (view *LiveView) PulseFrame(now ...float64) int {
	if len(now) > 0 {
		return int(now[0]/SpyLivePulseInterval) % SpyLivePulseFrames
	}
	return int((float64(time.Now().UnixNano())/float64(time.Second))/SpyLivePulseInterval) % SpyLivePulseFrames
}

func (view *LiveView) HasActivePulse() bool {
	if view.State.ServiceState() != "running" {
		return false
	}
	for _, row := range view.State.TopicRows() {
		if row.Samples > 0 {
			return true
		}
	}
	return false
}

func (view *LiveView) PrintSnapshot(status string) RenderedView {
	if status != "" {
		view.State.serviceState = status
	}
	snapshot := view.Render(view.PulseFrame())
	view.LastSnapshot = snapshot
	return snapshot
}

func (view *LiveView) Render(pulseFrame int) RenderedView {
	rows := view.State.TopicRows()
	active := 0
	for _, row := range rows {
		if row.Samples > 0 {
			active++
		}
	}
	status := view.State.ServiceState()
	if status == "running" && active == 0 {
		status = "running, waiting for samples"
	}
	header := SpyLiveHeader(view.Config, status, active, pulseFrame)
	if view.DatabusSecure {
		header.Warning = "secure"
	} else if hasDatabus(view.Config) {
		header.Warning = "not secure"
	}
	topics := make([]RenderedTopic, 0, len(rows))
	for _, row := range rows {
		if len(topics) >= SpyLiveTopicRows {
			break
		}
		topics = append(topics, RenderedTopic{
			Activity: sampleActivityChip(row, pulseFrame),
			Topic:    row.Topic,
			Type:     fallbackString(row.TypeName, "-"),
			Writers:  row.Writers,
			Readers:  row.Readers,
			Samples:  row.Samples,
			Status:   topicStatus(row),
		})
	}
	if len(topics) == 0 {
		topics = append(topics, RenderedTopic{Activity: "[dim]○[/dim]", Topic: "waiting", Type: "No topics discovered yet", Status: "waiting"})
	}
	recent := view.State.RecentSamples()
	samples := make([]RenderedSample, 0, minInt(len(recent), SpyLiveSampleRows))
	for index := len(recent) - 1; index >= 0 && len(samples) < SpyLiveSampleRows; index-- {
		event := recent[index]
		samples = append(samples, RenderedSample{Topic: event.Topic, Sample: event.Sample})
	}
	writers, readers := view.State.EndpointTotals()
	return RenderedView{
		Title:   SpyPanelTitle(),
		Header:  header,
		Topics:  topics,
		Samples: samples,
		Stats:   RenderedStats{DataWriters: writers, DataReaders: readers, Rows: view.State.StatisticsRows()},
		Border:  SpyOrange,
	}
}

func SpyPanelTitle() string {
	return "[bold] Connext Cloud Spy  [/bold]"
}

func SpyLiveHeader(config map[string]any, status string, activeTopicCount int, pulseFrame int) SummaryLine {
	return SummaryLine{Label: "databus", Status: spySummaryChip(status, activeTopicCount, pulseFrame), Target: fmt.Sprintf("%s / %s", configString(config, "databus"), nestedString(config, "templates", "app"))}
}

func sampleActivityChip(row TopicRow, pulseFrame int) string {
	if row.Samples > 0 {
		if pulseFrame%2 == 0 {
			return "[green]●[/green]"
		}
		return "[green]◉[/green]"
	}
	if row.Writers > 0 || row.Readers > 0 {
		return "[#5f819d]○[/]"
	}
	return "[dim]○[/dim]"
}

func topicStatus(row TopicRow) string {
	if row.Samples > 0 {
		return "receiving samples"
	}
	if row.Writers > 0 {
		return "writer discovered"
	}
	if row.Readers > 0 {
		return "reader discovered"
	}
	if row.LastEvent != "" {
		return row.LastEvent
	}
	return "waiting"
}

func spySummaryChip(status string, activeTopicCount int, pulseFrame int) string {
	short := strings.ToLower(status)
	if strings.HasPrefix(short, "running, waiting") {
		return "[#5f819d]○ waiting samples[/]"
	}
	if short == "running" {
		noun := "topics"
		if activeTopicCount == 1 {
			noun = "topic"
		}
		glyph := "●"
		if pulseFrame%2 != 0 {
			glyph = "◉"
		}
		return fmt.Sprintf("[green]%s receiving %d %s[/green]", glyph, activeTopicCount, noun)
	}
	if short == "starting" {
		return "[#5f819d]◐ starting[/]"
	}
	if short == "stopped" {
		return "[dim]◌ stopped[/dim]"
	}
	return fmt.Sprintf("[yellow]◌ %s[/yellow]", short)
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
	return renderANSIForSize(view, spyDefaultWidth, spyDefaultHeight)
}

func renderANSIForSize(view RenderedView, width int, height int) string {
	if width <= 0 {
		width = spyDefaultWidth
	}
	if height <= 0 {
		height = spyDefaultHeight
	}
	contentWidth := maxInt(24, width-4)
	summary := renderPanel(stripMarkup(view.Title), []string{formatSummaryLine(view.Header, contentWidth)}, width, summaryPanelTheme())
	topicLines := []string{formatTopicHeader(contentWidth)}
	for _, topic := range view.Topics {
		topicLines = append(topicLines, formatTopicLine(topic, contentWidth))
	}
	if len(view.Topics) == 0 {
		topicLines = append(topicLines, dim("No topics discovered yet"))
	}
	sampleLines := []string{}
	if len(view.Samples) == 0 {
		sampleLines = append(sampleLines, dim("Waiting for JSON samples..."))
	} else {
		for _, sample := range view.Samples {
			sampleLines = append(sampleLines, formatSampleLine(sample, contentWidth))
		}
	}
	statsLines := formatStatisticsLines(view.Stats, contentWidth)
	fixed := 1 + len(summary) + 1 + 2 + 1 + 2 + 1 + minInt(len(statsLines)+2, 6)
	available := maxInt(2, height-fixed)
	topicBudget := minInt(len(topicLines), maxInt(1, available/2))
	sampleBudget := minInt(len(sampleLines), maxInt(1, available-topicBudget))
	lines := []string{"\x1b[H\x1b[J"}
	lines = append(lines, summary...)
	lines = append(lines, "")
	lines = append(lines, renderPanel("Topics", resizeLines(topicLines, topicBudget), width, topicsPanelTheme())...)
	lines = append(lines, "")
	lines = append(lines, renderPanel("Samples", resizeLines(sampleLines, sampleBudget), width, samplesPanelTheme())...)
	if len(statsLines) > 0 {
		lines = append(lines, "")
		lines = append(lines, renderPanel("Statistics", resizeLines(statsLines, minInt(len(statsLines), 4)), width, topicsPanelTheme())...)
	}
	return strings.Join(lines, "\n")
}

func formatSummaryLine(line SummaryLine, contentWidth int) string {
	warningWidth := 0
	warning := ""
	if line.Warning != "" {
		warning = styleInlineWarning(line.Warning)
		warningWidth = displayWidth(warning) + 2
	}
	targetWidth := maxInt(8, contentWidth-spySummaryLabelWidth-spySummaryStatusWidth-warningWidth-4)
	formatted := fmt.Sprintf("%s  %s  %s", styleLabel(strings.ToUpper(line.Label), spySummaryLabelWidth), styleChipWidth(line.Status, spySummaryStatusWidth), styleTarget(truncateDisplay(line.Target, targetWidth), targetWidth))
	if warning != "" {
		formatted += "  " + warning
	}
	return formatted
}

func formatTopicHeader(contentWidth int) string {
	return fmt.Sprintf("%s  %s  %s  %s  %s  %s", dim(padDisplay("IO", 4)), dim(padDisplay("Topic", 24)), dim(padDisplay("Type", 18)), dim(padDisplay("W/R", 5)), dim(padDisplay("Samples", 7)), dim(padDisplay("Status", maxInt(10, contentWidth-70))))
}

func formatTopicLine(topic RenderedTopic, contentWidth int) string {
	return fmt.Sprintf("%s  %s  %s  %s  %s  %s",
		styleChipWidth(topic.Activity, 4),
		styleBold(truncateDisplay(topic.Topic, 24), 24),
		padDisplay(truncateDisplay(topic.Type, 18), 18),
		padDisplay(fmt.Sprintf("%d/%d", topic.Writers, topic.Readers), 5),
		padDisplay(fmt.Sprintf("%d", topic.Samples), 7),
		styleStatus(truncateDisplay(topic.Status, maxInt(10, contentWidth-70)), maxInt(10, contentWidth-70)))
}

func formatSampleLine(sample RenderedSample, contentWidth int) string {
	prefix := truncateDisplay(sample.Topic, 20)
	remaining := maxInt(8, contentWidth-displayWidth(prefix)-5)
	return styleBold(prefix, 20) + "  " + dim(truncateDisplay(sample.Sample, remaining))
}

func formatStatisticsLines(stats RenderedStats, contentWidth int) []string {
	if stats.DataWriters == 0 && stats.DataReaders == 0 && len(stats.Rows) == 0 {
		return nil
	}
	lines := []string{fmt.Sprintf("Discovered %d DataWriters and %d DataReaders", stats.DataWriters, stats.DataReaders)}
	for _, row := range stats.Rows {
		s := row.Statistics
		lines = append(lines, truncateDisplay(fmt.Sprintf("%d data, %d dispose, %d no-writers  %s (%s)", s.Data, s.Dispose, s.NoWriters, row.Topic, fallbackString(row.TypeName, "-")), contentWidth))
	}
	return lines
}

func renderPanel(title string, body []string, width int, theme panelTheme) []string {
	inner := maxInt(1, width-2)
	label := truncateDisplay(title, maxInt(1, inner-3))
	filler := maxInt(0, inner-displayWidth(label)-3)
	lines := []string{theme.borderStyle("┌─ ") + theme.titleStyle(label) + theme.borderStyle(" "+strings.Repeat("─", filler)+"┐")}
	if theme.paddedBody {
		lines = append(lines, panelBody("", width, theme.borderStyle))
	}
	for _, line := range body {
		lines = append(lines, panelBody(line, width, theme.borderStyle))
	}
	if theme.paddedBody {
		lines = append(lines, panelBody("", width, theme.borderStyle))
	}
	lines = append(lines, theme.borderStyle("└"+strings.Repeat("─", maxInt(1, width-2))+"┘"))
	return lines
}

func panelBody(content string, width int, borderStyle func(string) string) string {
	inner := maxInt(1, width-4)
	if displayWidth(content) > inner {
		content = truncateDisplay(content, inner)
	}
	return borderStyle("│ ") + padStyled(content, inner) + borderStyle(" │")
}

func resizeLines(lines []string, size int) []string {
	if size <= 0 {
		return nil
	}
	if len(lines) >= size {
		return append([]string(nil), lines[:size]...)
	}
	out := append([]string(nil), lines...)
	for len(out) < size {
		out = append(out, "")
	}
	return out
}

func styleTitle(value string) string        { return "\x1b[1;38;5;208m" + value + "\x1b[0m" }
func styleSection(value string) string      { return "\x1b[1;38;5;110m" + value + "\x1b[0m" }
func styleMutedTitle(value string) string   { return "\x1b[1;38;5;245m" + value + "\x1b[0m" }
func styleOrangeBorder(value string) string { return "\x1b[38;5;208m" + value + "\x1b[0m" }
func styleBlueBorder(value string) string   { return "\x1b[38;5;110m" + value + "\x1b[0m" }
func styleGrayBorder(value string) string   { return "\x1b[38;5;245m" + value + "\x1b[0m" }
func dim(value string) string               { return "\x1b[2m" + value + "\x1b[0m" }
func styleLabel(value string, width int) string {
	return "\x1b[2;38;5;110m" + padDisplay(value, width) + "\x1b[0m"
}
func styleTarget(value string, width int) string {
	return "\x1b[1m" + padDisplay(value, width) + "\x1b[0m"
}
func styleBold(value string, width int) string {
	return "\x1b[1m" + padDisplay(value, width) + "\x1b[0m"
}

func styleInlineWarning(value string) string {
	if value == "secure" {
		return "\x1b[38;2;95;129;157m(• " + value + ")\x1b[0m"
	}
	return "\x1b[33m(⚠ " + value + ")\x1b[0m"
}

func styleStatus(value string, width int) string {
	content := padDisplay(value, width)
	if strings.Contains(strings.ToLower(value), "receiving") {
		return "\x1b[32m" + content + "\x1b[0m"
	}
	return dim(content)
}

func styleChipWidth(markup string, width int) string {
	content := padDisplay(stripMarkup(markup), width)
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

func summaryPanelTheme() panelTheme {
	return panelTheme{titleStyle: styleTitle, borderStyle: styleOrangeBorder, paddedBody: true}
}

func topicsPanelTheme() panelTheme {
	return panelTheme{titleStyle: styleSection, borderStyle: styleBlueBorder}
}

func samplesPanelTheme() panelTheme {
	return panelTheme{titleStyle: styleMutedTitle, borderStyle: styleGrayBorder}
}

func padDisplay(value string, width int) string {
	clean := stripANSIEscapes(value)
	runes := []rune(clean)
	if len(runes) > width {
		return truncateDisplay(clean, width)
	}
	return clean + strings.Repeat(" ", width-len(runes))
}

func padStyled(value string, width int) string {
	visible := displayWidth(value)
	if visible >= width {
		return value
	}
	return value + strings.Repeat(" ", width-visible)
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

func stripMarkup(value string) string {
	replacements := []string{"[green]", "", "[/green]", "", "[yellow]", "", "[/yellow]", "", "[dim]", "", "[/dim]", "", "[#5f819d]", "", "[/]", "", "[bold]", "", "[/bold]", ""}
	replacer := strings.NewReplacer(replacements...)
	return replacer.Replace(value)
}

func stripANSIEscapes(value string) string {
	out := strings.Builder{}
	inEscape := false
	for _, r := range value {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func terminalSize(out io.Writer) (int, int) {
	file, ok := out.(*os.File)
	if ok {
		width, height, err := term.GetSize(int(file.Fd()))
		if err == nil {
			return width, height
		}
	}
	return spyDefaultWidth, spyDefaultHeight
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
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

func nestedString(config map[string]any, keys ...string) string {
	current := any(config)
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = asMap[key]
	}
	value, _ := current.(string)
	return value
}

func hasDatabus(config map[string]any) bool {
	return configString(config, "databus") != "none" && nestedString(config, "templates", "app") != ""
}
