package spy

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/tui"
)

const (
	SpyOrange               = tui.RTIOrange
	spyDefaultWidth         = 120
	spyDefaultHeight        = 40
	spySummaryLabelWidth    = 12
	spySummaryStatusWidth   = 24
	spySampleTimestampWidth = len("2006-01-02 15:04:05.000000")
)

type SummaryLine struct {
	Label   string
	Status  string
	Target  string
	Warning string
}

type RenderedTopic struct {
	Activity   string
	Topic      string
	Type       string
	Writers    int
	Readers    int
	Samples    int
	LastSample string
}

type RenderedSample struct {
	Time   string
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
			Activity:   sampleActivityChip(row, pulseFrame),
			Topic:      row.Topic,
			Type:       fallbackString(row.TypeName, "-"),
			Writers:    row.Writers,
			Readers:    row.Readers,
			Samples:    row.Samples,
			LastSample: formatLastSample(row.LatestTime, row.LatestJSON),
		})
	}
	if len(topics) == 0 {
		topics = append(topics, RenderedTopic{Activity: "[dim]○[/dim]", Topic: "waiting", Type: "No topics discovered yet", LastSample: "-"})
	}
	recent := view.State.RecentSamples()
	samples := make([]RenderedSample, 0, tui.MinInt(len(recent), SpyLiveSampleRows))
	for index := len(recent) - 1; index >= 0 && len(samples) < SpyLiveSampleRows; index-- {
		event := recent[index]
		samples = append(samples, RenderedSample{Time: event.Time, Topic: event.Topic, Sample: event.Sample})
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
		return fmt.Sprintf("[%s]○[/]", tui.RTIBlue)
	}
	return "[dim]○[/dim]"
}

func spySummaryChip(status string, activeTopicCount int, pulseFrame int) string {
	short := strings.ToLower(status)
	if strings.HasPrefix(short, "running, waiting") {
		return fmt.Sprintf("[%s]○ waiting samples[/]", tui.RTIBlue)
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
		return fmt.Sprintf("[%s]◐ starting[/]", tui.RTIBlue)
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
	width, height := tui.TerminalSize(renderer.Out, spyDefaultWidth, spyDefaultHeight)
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
	contentWidth := tui.MaxInt(24, width-4)
	summary := tui.RenderPanel(tui.StripMarkup(view.Title), []string{formatSummaryLine(view.Header, contentWidth)}, width, summaryPanelTheme())
	topicLines := []string{formatTopicHeader(contentWidth)}
	for _, topic := range view.Topics {
		topicLines = append(topicLines, formatTopicLine(topic, contentWidth))
	}
	if len(view.Topics) == 0 {
		topicLines = append(topicLines, tui.Dim("No topics discovered yet"))
	}
	sampleLines := []string{}
	if len(view.Samples) == 0 {
		sampleLines = append(sampleLines, tui.Dim("Waiting for JSON samples..."))
	} else {
		for _, sample := range view.Samples {
			sampleLines = append(sampleLines, formatSampleLine(sample, contentWidth))
		}
	}
	statsLines := formatStatisticsLines(view.Stats, contentWidth)
	fixed := 1 + len(summary) + 1 + 2 + 1 + 2 + 1 + tui.MinInt(len(statsLines)+2, 6)
	available := tui.MaxInt(2, height-fixed)
	topicBudget := tui.MinInt(len(topicLines), tui.MaxInt(1, available/2))
	sampleBudget := tui.MinInt(len(sampleLines), tui.MaxInt(1, available-topicBudget))
	lines := []string{"\x1b[H\x1b[J"}
	lines = append(lines, summary...)
	lines = append(lines, "")
	lines = append(lines, tui.RenderPanel("Topics", resizeLines(topicLines, topicBudget), width, topicsPanelTheme())...)
	lines = append(lines, "")
	lines = append(lines, tui.RenderPanel("Samples", resizeLines(sampleLines, sampleBudget), width, samplesPanelTheme())...)
	if len(statsLines) > 0 {
		lines = append(lines, "")
		lines = append(lines, tui.RenderPanel("Statistics", resizeLines(statsLines, tui.MinInt(len(statsLines), 4)), width, topicsPanelTheme())...)
	}
	return strings.Join(lines, "\n")
}

func formatSummaryLine(line SummaryLine, contentWidth int) string {
	warningWidth := 0
	warning := ""
	if line.Warning != "" {
		warning = tui.StyleInlineWarning(line.Warning)
		warningWidth = tui.DisplayWidth(warning) + 2
	}
	targetWidth := tui.MaxInt(8, contentWidth-spySummaryLabelWidth-spySummaryStatusWidth-warningWidth-4)
	formatted := fmt.Sprintf("%s  %s  %s", tui.StyleLabel(strings.ToUpper(line.Label), spySummaryLabelWidth), tui.StyleChipWidth(line.Status, spySummaryStatusWidth), tui.StyleTarget(tui.TruncateDisplay(line.Target, targetWidth), targetWidth))
	if warning != "" {
		formatted += "  " + warning
	}
	return formatted
}

func formatTopicHeader(contentWidth int) string {
	return fmt.Sprintf("%s  %s  %s  %s  %s  %s", tui.Dim(tui.PadDisplay("IO", 4)), tui.Dim(tui.PadDisplay("Topic", 24)), tui.Dim(tui.PadDisplay("Type", 18)), tui.Dim(tui.PadDisplay("W/R", 5)), tui.Dim(tui.PadDisplay("Samples", 7)), tui.Dim(tui.PadDisplay("Last sample", tui.MaxInt(12, contentWidth-70))))
}

func formatTopicLine(topic RenderedTopic, contentWidth int) string {
	return fmt.Sprintf("%s  %s  %s  %s  %s  %s",
		tui.StyleChipWidth(topic.Activity, 4),
		tui.StyleBold(tui.TruncateDisplay(topic.Topic, 24), 24),
		tui.PadDisplay(tui.TruncateDisplay(topic.Type, 18), 18),
		tui.PadDisplay(fmt.Sprintf("%d/%d", topic.Writers, topic.Readers), 5),
		tui.PadDisplay(fmt.Sprintf("%d", topic.Samples), 7),
		tui.Dim(tui.PadDisplay(tui.TruncateDisplay(topic.LastSample, tui.MaxInt(12, contentWidth-70)), tui.MaxInt(12, contentWidth-70))))
}

func formatSampleLine(sample RenderedSample, contentWidth int) string {
	timeLabel := tui.TruncateDisplay(fallbackString(sample.Time, "-"), spySampleTimestampWidth)
	topicLabel := tui.TruncateDisplay(sample.Topic, 16)
	remaining := tui.MaxInt(8, contentWidth-tui.DisplayWidth(timeLabel)-tui.DisplayWidth(topicLabel)-7)
	return tui.Dim(tui.PadDisplay(timeLabel, spySampleTimestampWidth)) + "  " + tui.StyleBold(topicLabel, 16) + "  " + tui.Dim(tui.TruncateDisplay(sample.Sample, remaining))
}

func formatLastSample(timestamp string, sample string) string {
	trimmedSample := strings.TrimSpace(sample)
	if trimmedSample == "" {
		return "-"
	}
	trimmedTime := stripSampleSubseconds(timestamp)
	if trimmedTime == "" {
		return trimmedSample
	}
	return trimmedTime + " " + trimmedSample
}

func stripSampleSubseconds(timestamp string) string {
	trimmed := strings.TrimSpace(timestamp)
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, ".", 2)
	return parts[0]
}

func formatStatisticsLines(stats RenderedStats, contentWidth int) []string {
	if stats.DataWriters == 0 && stats.DataReaders == 0 && len(stats.Rows) == 0 {
		return nil
	}
	lines := []string{fmt.Sprintf("Discovered %d DataWriters and %d DataReaders", stats.DataWriters, stats.DataReaders)}
	for _, row := range stats.Rows {
		s := row.Statistics
		lines = append(lines, tui.TruncateDisplay(fmt.Sprintf("%d data, %d dispose, %d no-writers  %s (%s)", s.Data, s.Dispose, s.NoWriters, row.Topic, fallbackString(row.TypeName, "-")), contentWidth))
	}
	return lines
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
func summaryPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleTitle, BorderStyle: tui.StyleOrangeBorder, PaddedBody: true}
}

func topicsPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleSection, BorderStyle: tui.StyleBlueBorder}
}

func samplesPanelTheme() tui.PanelTheme {
	return tui.PanelTheme{TitleStyle: tui.StyleMutedSection, BorderStyle: tui.StyleGrayBorder}
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
