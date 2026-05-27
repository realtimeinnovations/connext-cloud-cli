package spy

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	SpyLiveTopicRows     = 16
	SpyLiveSampleRows    = 10
	SpyLivePulseInterval = 0.5
	SpyLivePulseFrames   = 4
	SpyLiveLogLines      = 40
)

type TopicRow struct {
	Topic      string
	TypeName   string
	Writers    int
	Readers    int
	Samples    int
	LastEvent  string
	LatestTime string
	LatestJSON string
	Statistics SampleStatistics
}

type SampleEvent struct {
	Time     string
	Topic    string
	TypeName string
	Sample   string
	Action   string
}

type SampleStatistics struct {
	Data      int
	Dispose   int
	NoWriters int
}

type ParticipantInfo struct {
	Source    string
	Name      string
	HostName  string
	ProcessID string
}

type SpyState struct {
	topics       map[string]*TopicRow
	recent       []SampleEvent
	participants map[string]ParticipantInfo
	serviceState string
	maxSamples   int
	stats        map[string]SampleStatistics
	dataWriters  int
	dataReaders  int
	inStats      bool
}

var (
	spyRunningRE     = regexp.MustCompile(`rtiddsspy is listening for data`)
	spyParticipantRE = regexp.MustCompile(`(New|Deleted) participant\s+from\s+(\S+)\s+:\s+(.*)`)
	spyAttrRE        = regexp.MustCompile(`(\w+)="([^"]*)"`)
	spyEndpointRE    = regexp.MustCompile(`\d{4}-\d{2}-\d{2} .* New (writer|reader)\s+from\s+(\S+)\s+:\s+topic="([^"]+)" type="([^"]+)"`)
	spyDataRE        = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+(New data|Modified instance|Disposed instance|No writers)\s+from\s+(\S+)\s+:\s+topic="([^"]+)" type="([^"]+)" sample=(.*)`)
	spyStatsHeaderRE = regexp.MustCompile(`Discovered\s+(\d+)\s+DataWriters\s+and\s+(\d+)\s+DataReaders`)
	spyStatsTopicRE  = regexp.MustCompile(`^\s*(\d+),\s*(\d+),\s*(\d+)\s+\(Topic="([^"]+)"\s+Type="([^"]+)"\)`)
)

func NewSpyState(maxSamples int) *SpyState {
	if maxSamples <= 0 {
		maxSamples = SpyLiveLogLines
	}
	return &SpyState{
		topics:       map[string]*TopicRow{},
		recent:       make([]SampleEvent, 0, maxSamples),
		participants: map[string]ParticipantInfo{},
		serviceState: "starting",
		maxSamples:   maxSamples,
		stats:        map[string]SampleStatistics{},
	}
}

func (state *SpyState) ServiceState() string {
	return state.serviceState
}

func (state *SpyState) EndpointTotals() (int, int) {
	return state.dataWriters, state.dataReaders
}

func (state *SpyState) ConnectedHostNames() []string {
	hosts := map[string]struct{}{}
	for _, participant := range state.participants {
		if participant.HostName != "" {
			hosts[participant.HostName] = struct{}{}
		}
	}
	names := make([]string, 0, len(hosts))
	for host := range hosts {
		names = append(names, host)
	}
	sort.Strings(names)
	return names
}

func (state *SpyState) Update(line string) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return
	}
	if spyRunningRE.MatchString(line) {
		state.serviceState = "running"
	}
	if trimmed == "---- Statistics ----" {
		state.inStats = true
		state.serviceState = "stopped"
		return
	}
	if state.inStats {
		state.updateStatistics(line)
		return
	}
	if match := spyParticipantRE.FindStringSubmatch(line); match != nil {
		state.updateParticipant(match[1], match[2], match[3])
		return
	}
	if match := spyEndpointRE.FindStringSubmatch(line); match != nil {
		state.updateEndpoint(match[1], match[3], match[4])
		return
	}
	if match := spyDataRE.FindStringSubmatch(line); match != nil {
		state.updateSample(match[1], match[2], match[4], match[5], match[6])
	}
}

func PlainEventLines(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	if match := spyEndpointRE.FindStringSubmatch(line); match != nil {
		kind, source, topic, typeName := match[1], match[2], match[3], match[4]
		if strings.HasPrefix(topic, "DDS") {
			return nil
		}
		return []string{"[" + kind + "] " + topic + " " + typeName + " from " + source}
	}
	if match := spyDataRE.FindStringSubmatch(line); match != nil {
		action, topic, typeName, sample := match[2], match[4], match[5], match[6]
		if strings.HasPrefix(topic, "DDS") {
			return nil
		}
		tag := "data"
		switch action {
		case "Disposed instance":
			tag = "dispose"
		case "No writers":
			tag = "no_writers"
		}
		return []string{"[" + tag + "] " + topic + " " + typeName + " " + compactJSON(sample)}
	}
	if match := spyStatsHeaderRE.FindStringSubmatch(line); match != nil {
		return []string{"[stats] writers=" + match[1] + " readers=" + match[2]}
	}
	if match := spyStatsTopicRE.FindStringSubmatch(line); match != nil {
		return []string{"[stats] " + match[4] + " " + match[5] + " data=" + match[1] + " dispose=" + match[2] + " no_writers=" + match[3]}
	}
	if spyRunningRE.MatchString(line) {
		return []string{"[status] running"}
	}
	return nil
}

func (state *SpyState) TopicRows() []TopicRow {
	rows := make([]TopicRow, 0, len(state.topics))
	for _, row := range state.topics {
		copyRow := *row
		if stats, ok := state.stats[row.Topic]; ok {
			copyRow.Statistics = stats
			if copyRow.Samples < stats.Data {
				copyRow.Samples = stats.Data
			}
		}
		rows = append(rows, copyRow)
	}
	sort.Slice(rows, func(i, j int) bool {
		leftLive := rows[i].Samples > 0
		rightLive := rows[j].Samples > 0
		if leftLive != rightLive {
			return leftLive
		}
		return strings.ToLower(rows[i].Topic) < strings.ToLower(rows[j].Topic)
	})
	return rows
}

func (state *SpyState) RecentSamples() []SampleEvent {
	return append([]SampleEvent(nil), state.recent...)
}

func (state *SpyState) StatisticsRows() []TopicRow {
	rows := state.TopicRows()
	filtered := make([]TopicRow, 0, len(rows))
	for _, row := range rows {
		if _, ok := state.stats[row.Topic]; ok {
			filtered = append(filtered, row)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		left := filtered[i].Statistics.Data + filtered[i].Statistics.Dispose + filtered[i].Statistics.NoWriters
		right := filtered[j].Statistics.Data + filtered[j].Statistics.Dispose + filtered[j].Statistics.NoWriters
		if left != right {
			return left > right
		}
		return strings.ToLower(filtered[i].Topic) < strings.ToLower(filtered[j].Topic)
	})
	return filtered
}

func (state *SpyState) updateEndpoint(kind string, topic string, typeName string) {
	if strings.HasPrefix(topic, "DDS") {
		return
	}
	row := state.topic(topic, typeName)
	if kind == "writer" {
		row.Writers++
		row.LastEvent = "writer discovered"
	} else {
		row.Readers++
		row.LastEvent = "reader discovered"
	}
}

func (state *SpyState) updateParticipant(action string, source string, attributes string) {
	values := map[string]string{}
	for _, match := range spyAttrRE.FindAllStringSubmatch(attributes, -1) {
		values[match[1]] = match[2]
	}
	hostName := values["hostName"]
	if hostName == "" {
		return
	}
	key := hostName + "\x00" + values["processId"]
	if key == hostName+"\x00" {
		key = hostName + "\x00" + source
	}
	if action == "Deleted" {
		delete(state.participants, key)
		return
	}
	state.participants[key] = ParticipantInfo{
		Source:    source,
		Name:      values["name"],
		HostName:  hostName,
		ProcessID: values["processId"],
	}
}

func (state *SpyState) updateSample(timestamp string, action string, topic string, typeName string, sample string) {
	if strings.HasPrefix(topic, "DDS") {
		return
	}
	row := state.topic(topic, typeName)
	row.Samples++
	row.LastEvent = strings.ToLower(action)
	row.LatestTime = timestamp
	row.LatestJSON = compactJSON(sample)
	state.recent = append(state.recent, SampleEvent{Time: timestamp, Topic: topic, TypeName: typeName, Sample: row.LatestJSON, Action: row.LastEvent})
	if len(state.recent) > state.maxSamples {
		state.recent = append([]SampleEvent(nil), state.recent[len(state.recent)-state.maxSamples:]...)
	}
}

func (state *SpyState) updateStatistics(line string) {
	if match := spyStatsHeaderRE.FindStringSubmatch(line); match != nil {
		state.dataWriters = parseInt(match[1])
		state.dataReaders = parseInt(match[2])
		return
	}
	if match := spyStatsTopicRE.FindStringSubmatch(line); match != nil {
		topic, typeName := match[4], match[5]
		row := state.topic(topic, typeName)
		stats := SampleStatistics{Data: parseInt(match[1]), Dispose: parseInt(match[2]), NoWriters: parseInt(match[3])}
		state.stats[topic] = stats
		row.Statistics = stats
		row.Samples = maxInt(row.Samples, stats.Data)
	}
}

func (state *SpyState) topic(topic string, typeName string) *TopicRow {
	if existing, ok := state.topics[topic]; ok {
		if existing.TypeName == "" {
			existing.TypeName = typeName
		}
		return existing
	}
	row := &TopicRow{Topic: topic, TypeName: typeName, LastEvent: "discovered"}
	state.topics[topic] = row
	return row
}

func compactJSON(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return trimmed
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return trimmed
	}
	return string(encoded)
}

func parseInt(value string) int {
	parsed, _ := strconv.Atoi(value)
	return parsed
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}
