package gateway

import (
	"encoding/xml"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/realtimeinnovations/connext-cloud-cli/internal/connext"
)

const (
	RoutingLiveLogLines      = 18
	RoutingLivePulseInterval = 0.5
	RoutingLivePulseFrames   = 4
)

type ConnextInstall = connext.Install

type RouteState struct {
	Route            string
	Topic            string
	Direction        string
	State            string
	InputMatched     bool
	OutputMatched    bool
	SourceConnection string
	TypeName         string
}

type TopicRouteRow struct {
	Topic       string
	TypeName    string
	EdgeToCloud string
	CloudToEdge string
	LastEvent   string
}

type routeKey struct {
	Route string
	Topic string
}

type discoveredKey struct {
	Topic string
	Side  string
}

type discoveredValue struct {
	TypeName   string
	Connection string
}

type RoutingState struct {
	routes            map[routeKey]*RouteState
	discoveredStreams map[discoveredKey]discoveredValue
	recentLogs        []string
	topicEvents       map[string]string
	serviceState      string
	maxLogs           int
}

var (
	streamDiscoveredRE   = regexp.MustCompile(`STREAM_DISCOVERED\] name=([^,]+), type_name=([^,]+), connection=(\S+)`)
	streamDisposedRE     = regexp.MustCompile(`STREAM_DISPOSED\] name=([^,]+), type_name=([^,]+), connection=(\S+)`)
	routeEventRE         = regexp.MustCompile(routeRef() + `\|(CREATE|ENABLE|START|RUN|PAUSE|STOP|DISABLE|DELETE)`)
	routeInputMatchRE    = regexp.MustCompile(routeRef() + `\] Input\d+ matched publication stream=(\S+)`)
	routeOutputMatchRE   = regexp.MustCompile(routeRef() + `\] Output\d+ matched subscription stream=(\S+)`)
	routeInputLostRE     = regexp.MustCompile(routeRef() + `\] Input\d+ lost publication stream=(\S+)`)
	routeOutputLostRE    = regexp.MustCompile(routeRef() + `\] Output\d+ lost subscription stream=(\S+)`)
	routeInputEnableRE   = regexp.MustCompile(routeRef() + `/inputs/Input\d+\|ENABLE\] stream=(\S+)`)
	routeOutputEnableRE  = regexp.MustCompile(routeRef() + `/outputs/Output\d+\|ENABLE\] stream=(\S+)`)
	routeInputDisableRE  = regexp.MustCompile(routeRef() + `/inputs/Input\d+\|DISABLE\] stream=(\S+)`)
	routeOutputDisableRE = regexp.MustCompile(routeRef() + `/outputs/Output\d+\|DISABLE\] stream=(\S+)`)
	warningTopicRE       = regexp.MustCompile(`Topic=([^,}]+)`)
	serviceRunningRE     = regexp.MustCompile(`RTI Routing Service .* executing`)
	versionRE            = regexp.MustCompile(`(\d+\.\d+\.\d+)`)
	routeRefRE           = regexp.MustCompile(routeRef())
)

func routeRef() string {
	return `/routes/(?:"([^@"]+)@([^"]+)"|([^@|/\]]+)@([^|/\]]+))`
}

func NewRoutingState(maxLogs int) *RoutingState {
	if maxLogs <= 0 {
		maxLogs = RoutingLiveLogLines
	}
	return &RoutingState{
		routes:            map[routeKey]*RouteState{},
		discoveredStreams: map[discoveredKey]discoveredValue{},
		recentLogs:        make([]string, 0, maxLogs),
		topicEvents:       map[string]string{},
		serviceState:      "starting",
		maxLogs:           maxLogs,
	}
}

func (state *RoutingState) ServiceState() string {
	return state.serviceState
}

func (state *RoutingState) Routes() map[string]RouteState {
	copyMap := map[string]RouteState{}
	for key, route := range state.routes {
		copyMap[key.Route+"@"+key.Topic] = *route
	}
	return copyMap
}

func (state *RoutingState) RecentLogs() []string {
	return append([]string(nil), state.recentLogs...)
}

func (state *RoutingState) Update(line string) {
	if serviceRunningRE.MatchString(line) {
		state.serviceState = "running"
	}
	state.updateStreamDiscovery(line)
	state.updateStreamDisposal(line)
	state.updateRouteEvent(line)
	state.updateRouteMatch(line)
	state.updateRouteLost(line)
	state.updateWarningEvent(line)
	if state.isLiveLogLine(line) {
		state.appendLog(state.summarizeLogLine(line))
	}
}

func PlainEventLines(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.Contains(line, "RTIXMLUTILSTransformer_transformWithParams") {
		return nil
	}
	if serviceRunningRE.MatchString(line) {
		return []string{"[status] running"}
	}
	if match := streamDiscoveredRE.FindStringSubmatch(line); match != nil {
		topic, typeName, connection := match[1], match[2], match[3]
		if strings.HasPrefix(topic, "DDS") {
			return nil
		}
		return []string{"[stream] " + topic + " " + typeName + " discovered from " + connection}
	}
	if match := streamDisposedRE.FindStringSubmatch(line); match != nil {
		topic, typeName, connection := match[1], match[2], match[3]
		if strings.HasPrefix(topic, "DDS") {
			return nil
		}
		return []string{"[stream] " + topic + " " + typeName + " disposed from " + connection}
	}
	if match := routeEventRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		event := strings.ToLower(match[len(match)-1])
		return []string{"[route] " + topic + " " + RouteDirection(routeName) + " " + event}
	}
	if match := routeInputMatchRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return []string{"[match] " + topic + " " + RouteDirection(routeName) + " input"}
	}
	if match := routeOutputMatchRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return []string{"[match] " + topic + " " + RouteDirection(routeName) + " output"}
	}
	if match := routeInputEnableRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return []string{"[match] " + topic + " " + RouteDirection(routeName) + " input"}
	}
	if match := routeOutputEnableRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return []string{"[match] " + topic + " " + RouteDirection(routeName) + " output"}
	}
	if match := routeInputLostRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return []string{"[lost] " + topic + " " + RouteDirection(routeName) + " input"}
	}
	if match := routeOutputLostRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return []string{"[lost] " + topic + " " + RouteDirection(routeName) + " output"}
	}
	if match := routeInputDisableRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return []string{"[lost] " + topic + " " + RouteDirection(routeName) + " input"}
	}
	if match := routeOutputDisableRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return []string{"[lost] " + topic + " " + RouteDirection(routeName) + " output"}
	}
	if strings.Contains(line, "WARNING ") {
		return []string{"[warning] " + strings.TrimPrefix(trimLogContext(line, "WARNING"), "WARNING: ")}
	}
	if strings.Contains(line, "ERROR ") {
		return []string{"[error] " + strings.TrimPrefix(trimLogContext(line, "ERROR"), "ERROR: ")}
	}
	return nil
}

func (state *RoutingState) SeedFromConfig(xmlPath string) {
	data, err := os.ReadFile(xmlPath)
	if err != nil {
		return
	}
	var root xmlNode
	if err := xml.Unmarshal(data, &root); err != nil {
		return
	}
	for _, routeElement := range root.walkRoutes() {
		routeName := routeElement.attr("name")
		if routeName == "" {
			continue
		}
		topic := routeTopicFilter(routeElement)
		route := state.route(routeName, topic)
		route.State = "LISTENING"
		if _, exists := state.topicEvents[topic]; !exists {
			state.topicEvents[topic] = "listening"
		}
	}
}

func (state *RoutingState) TopicRows() []TopicRouteRow {
	topics := map[string]struct{}{}
	for _, route := range state.routes {
		topics[route.Topic] = struct{}{}
	}
	if len(topics) == 0 {
		return nil
	}
	orderedTopics := make([]string, 0, len(topics))
	for topic := range topics {
		orderedTopics = append(orderedTopics, topic)
	}
	sort.Slice(orderedTopics, func(i, j int) bool {
		left, right := orderedTopics[i], orderedTopics[j]
		if left == "*" && right != "*" {
			return false
		}
		if left != "*" && right == "*" {
			return true
		}
		return left < right
	})

	rows := make([]TopicRouteRow, 0, len(orderedTopics))
	for _, topic := range orderedTopics {
		topicRoutes := make([]*RouteState, 0)
		var typeName string
		for _, route := range state.routes {
			if route.Topic != topic {
				continue
			}
			topicRoutes = append(topicRoutes, route)
			if typeName == "" && route.TypeName != "" {
				typeName = route.TypeName
			}
		}
		if typeName == "" {
			for key, discovered := range state.discoveredStreams {
				if key.Topic == topic {
					typeName = discovered.TypeName
					break
				}
			}
		}
		lastEvent := state.topicEvents[topic]
		if lastEvent == "" {
			lastEvent = "waiting for topics"
		}
		rows = append(rows, TopicRouteRow{
			Topic:       topic,
			TypeName:    typeName,
			EdgeToCloud: laneState(topicRoutes, "edge_to_cloud"),
			CloudToEdge: laneState(topicRoutes, "cloud_to_edge"),
			LastEvent:   lastEvent,
		})
	}
	return rows
}

func (state *RoutingState) route(routeName string, topic string) *RouteState {
	key := routeKey{Route: routeName, Topic: topic}
	if existing, ok := state.routes[key]; ok {
		return existing
	}
	direction := RouteDirection(routeName)
	sourceSide := "edge"
	if direction == "cloud_to_edge" {
		sourceSide = "cloud"
	}
	discovered := state.discoveredStreams[discoveredKey{Topic: topic, Side: sourceSide}]
	route := &RouteState{
		Route:            routeName,
		Topic:            topic,
		Direction:        direction,
		State:            "CREATE",
		TypeName:         discovered.TypeName,
		SourceConnection: discovered.Connection,
	}
	state.routes[key] = route
	return route
}

func (state *RoutingState) updateStreamDiscovery(line string) {
	match := streamDiscoveredRE.FindStringSubmatch(line)
	if match == nil {
		return
	}
	topic, typeName, connection := match[1], match[2], match[3]
	if strings.HasPrefix(topic, "DDS") {
		return
	}
	side := "edge"
	if connection == "cloud" {
		side = "cloud"
	}
	state.discoveredStreams[discoveredKey{Topic: topic, Side: side}] = discoveredValue{TypeName: typeName, Connection: connection}
	state.topicEvents[topic] = side + " discovered"
	for _, route := range state.routes {
		sourceSide := "edge"
		if route.Direction == "cloud_to_edge" {
			sourceSide = "cloud"
		}
		if route.Topic == topic && sourceSide == side {
			route.TypeName = typeName
			route.SourceConnection = connection
		}
	}
}

func (state *RoutingState) updateStreamDisposal(line string) {
	match := streamDisposedRE.FindStringSubmatch(line)
	if match == nil {
		return
	}
	topic, connection := match[1], match[3]
	side := "edge"
	if connection == "cloud" {
		side = "cloud"
	}
	delete(state.discoveredStreams, discoveredKey{Topic: topic, Side: side})
	state.topicEvents[topic] = side + " disposed"
}

func (state *RoutingState) updateRouteEvent(line string) {
	matches := routeEventRE.FindAllStringSubmatch(line, -1)
	for _, match := range matches {
		routeName, topic := routeMatchParts(match)
		route := state.route(routeName, topic)
		event := match[len(match)-1]
		switch event {
		case "PAUSE":
			route.State = "STOPPING"
		case "STOP", "DISABLE":
			route.State = "STOPPED"
		case "DELETE":
			route.State = "DELETED"
		default:
			route.State = MaxRouteState(route.State, event)
		}
		if event == "DELETE" {
			state.topicEvents[route.Topic] = "route removed"
		} else if event == "RUN" {
			state.topicEvents[route.Topic] = routingDirectionStatus(route.Direction)
		} else if event == "START" {
			state.topicEvents[route.Topic] = "starting"
		} else if !isEndpointStatus(state.topicEvents[route.Topic]) && !isProblemStatus(state.topicEvents[route.Topic]) {
			state.topicEvents[route.Topic] = "waiting for endpoint"
		}
		if event == "DISABLE" || event == "DELETE" {
			route.InputMatched = false
			route.OutputMatched = false
		}
	}
}

func (state *RoutingState) updateRouteMatch(line string) {
	for _, candidate := range []struct {
		re   *regexp.Regexp
		attr string
	}{
		{routeInputMatchRE, "input"},
		{routeOutputMatchRE, "output"},
		{routeInputEnableRE, "input"},
		{routeOutputEnableRE, "output"},
	} {
		match := candidate.re.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		routeName, topic := routeMatchParts(match)
		route := state.route(routeName, topic)
		if candidate.attr == "input" {
			route.InputMatched = true
		} else {
			route.OutputMatched = true
		}
		route.State = MaxRouteState(route.State, "ENABLE")
		if !isProblemStatus(state.topicEvents[route.Topic]) {
			state.topicEvents[route.Topic] = endpointStatus(route, candidate.attr, "found")
		}
	}
}

func (state *RoutingState) updateRouteLost(line string) {
	for _, candidate := range []struct {
		re   *regexp.Regexp
		attr string
	}{
		{routeInputLostRE, "input"},
		{routeOutputLostRE, "output"},
	} {
		match := candidate.re.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		routeName, topic := routeMatchParts(match)
		route := state.route(routeName, topic)
		if candidate.attr == "input" {
			route.InputMatched = false
		} else {
			route.OutputMatched = false
		}
		route.State = "STOPPING"
		state.topicEvents[route.Topic] = endpointStatus(route, candidate.attr, "lost")
	}
	for _, candidate := range []struct {
		re   *regexp.Regexp
		attr string
	}{
		{routeInputDisableRE, "input"},
		{routeOutputDisableRE, "output"},
	} {
		match := candidate.re.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		routeName, topic := routeMatchParts(match)
		route := state.route(routeName, topic)
		if candidate.attr == "input" {
			route.InputMatched = false
		} else {
			route.OutputMatched = false
		}
		route.State = "STOPPED"
		if !isEndpointStatus(state.topicEvents[route.Topic]) && !isProblemStatus(state.topicEvents[route.Topic]) {
			state.topicEvents[route.Topic] = "waiting for endpoint"
		}
	}
}

func (state *RoutingState) updateWarningEvent(line string) {
	if !strings.Contains(line, "WARNING ") {
		return
	}
	status := ""
	switch {
	case strings.Contains(line, "INCOMPATIBLE QOS") || strings.Contains(line, "incompatible QoS"):
		status = "QoS mismatch"
	case strings.Contains(line, "FAILED TO GET") || strings.Contains(line, "type representation"):
		status = "type info missing"
	}
	if status == "" {
		return
	}
	routeMatch := routeRefRE.FindStringSubmatch(line)
	if routeMatch != nil {
		_, topic := routeMatchParts(routeMatch)
		state.topicEvents[topic] = status
		return
	}
	topicMatch := warningTopicRE.FindStringSubmatch(line)
	if topicMatch != nil {
		state.topicEvents[topicMatch[1]] = status
	}
}

func (state *RoutingState) isLiveLogLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if strings.Contains(line, "RTIXMLUTILSTransformer_transformWithParams") {
		return false
	}
	if strings.HasPrefix(strings.TrimLeft(line, " \t"), "<") || strings.HasPrefix(strings.TrimLeft(line, " \t"), "</") || strings.HasPrefix(strings.TrimLeft(line, " \t"), "<?") {
		return false
	}
	return strings.Contains(line, "WARNING ") ||
		strings.Contains(line, "ERROR ") ||
		strings.Contains(line, "STREAM_DISCOVERED") ||
		strings.Contains(line, "STREAM_DISPOSED") ||
		strings.Contains(line, "/routes/") ||
		strings.Contains(line, "RTI Routing Service")
}

func (state *RoutingState) summarizeLogLine(line string) string {
	if match := streamDiscoveredRE.FindStringSubmatch(line); match != nil && !strings.HasPrefix(match[1], "DDS") {
		return "discovered " + match[1] + " (" + match[2] + ") on " + match[3]
	}
	if match := streamDisposedRE.FindStringSubmatch(line); match != nil && !strings.HasPrefix(match[1], "DDS") {
		return "disposed " + match[1] + " (" + match[2] + ") on " + match[3]
	}
	if match := routeEventRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return strings.ToLower(match[len(match)-1]) + " " + routeName + "@" + topic
	}
	if match := routeInputMatchRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return "input matched " + routeName + "@" + topic
	}
	if match := routeOutputMatchRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return "output matched " + routeName + "@" + topic
	}
	if match := routeInputLostRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return "input lost " + routeName + "@" + topic
	}
	if match := routeOutputLostRE.FindStringSubmatch(line); match != nil {
		routeName, topic := routeMatchParts(match)
		return "output lost " + routeName + "@" + topic
	}
	if strings.Contains(line, "WARNING ") {
		return trimLogContext(line, "WARNING")
	}
	if strings.Contains(line, "ERROR ") {
		return trimLogContext(line, "ERROR")
	}
	return strings.TrimSpace(line)
}

func (state *RoutingState) appendLog(line string) {
	state.recentLogs = append(state.recentLogs, line)
	if len(state.recentLogs) > state.maxLogs {
		state.recentLogs = append([]string(nil), state.recentLogs[len(state.recentLogs)-state.maxLogs:]...)
	}
}

func RouteDirection(routeName string) string {
	switch {
	case strings.Contains(routeName, "edge_to_cloud"):
		return "edge_to_cloud"
	case strings.Contains(routeName, "cloud_to_edge"):
		return "cloud_to_edge"
	default:
		return "unknown"
	}
}

func VisibleTopicRows(rows []TopicRouteRow) []TopicRouteRow {
	concreteRows := make([]TopicRouteRow, 0)
	for _, row := range rows {
		if row.Topic != "*" {
			concreteRows = append(concreteRows, row)
		}
	}
	if len(concreteRows) > 0 {
		return concreteRows
	}
	return rows
}

func SortTopicRowsForDisplay(rows []TopicRouteRow) []TopicRouteRow {
	sortedRows := append([]TopicRouteRow(nil), rows...)
	sort.Slice(sortedRows, func(i, j int) bool {
		leftRank := topicRouteRank(sortedRows[i])
		rightRank := topicRouteRank(sortedRows[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return strings.ToLower(sortedRows[i].Topic) < strings.ToLower(sortedRows[j].Topic)
	})
	return sortedRows
}

func topicRouteRank(row TopicRouteRow) int {
	edgeLive := row.EdgeToCloud == "live"
	cloudLive := row.CloudToEdge == "live"
	switch {
	case edgeLive && cloudLive:
		return 0
	case edgeLive || cloudLive:
		return 1
	case rowHasRetainedRoute(row):
		return 2
	case rowHasStoppedRoute(row):
		return 3
	default:
		return 4
	}
}

func rowHasRetainedRoute(row TopicRouteRow) bool {
	retainedStates := map[string]bool{"starting": true, "ready": true, "listening": true}
	return retainedStates[row.EdgeToCloud] || retainedStates[row.CloudToEdge]
}

func rowHasStoppedRoute(row TopicRouteRow) bool {
	return map[string]bool{"stopping": true, "stopped": true}[row.EdgeToCloud] || map[string]bool{"stopping": true, "stopped": true}[row.CloudToEdge]
}

func endpointStatus(route *RouteState, endpointAttr string, action string) string {
	endpoint := "endpoint"
	switch route.Direction {
	case "edge_to_cloud":
		if endpointAttr == "input" {
			endpoint = "edge publisher"
		} else {
			endpoint = "cloud subscriber"
		}
	case "cloud_to_edge":
		if endpointAttr == "input" {
			endpoint = "cloud publisher"
		} else {
			endpoint = "edge subscriber"
		}
	}
	return endpoint + " " + action
}

func routingDirectionStatus(direction string) string {
	switch direction {
	case "edge_to_cloud":
		return "routing upstream"
	case "cloud_to_edge":
		return "routing downstream"
	default:
		return "routing"
	}
}

func isEndpointStatus(status string) bool {
	return strings.HasSuffix(status, " found") || strings.HasSuffix(status, " lost")
}

func isProblemStatus(status string) bool {
	return status == "QoS mismatch" || status == "type info missing"
}

func routeMatchParts(match []string) (string, string) {
	if len(match) >= 3 && match[1] != "" {
		return match[1], match[2]
	}
	if len(match) >= 5 && match[3] != "" {
		return match[3], match[4]
	}
	return "", ""
}

func laneState(routes []*RouteState, direction string) string {
	states := map[string]bool{}
	found := false
	for _, route := range routes {
		if route.Direction != direction {
			continue
		}
		found = true
		states[route.State] = true
	}
	if !found {
		return "waiting"
	}
	switch {
	case states["RUN"]:
		return "live"
	case states["START"]:
		return "starting"
	case states["CREATE"] || states["ENABLE"]:
		return "ready"
	case states["STOPPING"]:
		return "stopping"
	case states["STOPPED"] || states["DELETED"]:
		return "stopped"
	case states["LISTENING"]:
		return "listening"
	default:
		return "waiting"
	}
}

func MaxRouteState(current string, next string) string {
	order := map[string]int{
		"DELETED":   0,
		"STOPPED":   1,
		"STOPPING":  2,
		"LISTENING": 3,
		"CREATE":    4,
		"ENABLE":    5,
		"START":     6,
		"RUN":       7,
	}
	if order[next] >= order[current] {
		return next
	}
	return current
}

func trimLogContext(line string, level string) string {
	tail := line
	if idx := strings.Index(line, "] "); idx >= 0 {
		tail = line[idx+2:]
	}
	if len(tail) > 160 {
		tail = tail[:157] + "..."
	}
	return level + ": " + tail
}

type xmlNode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Nodes   []xmlNode  `xml:",any"`
	Text    string     `xml:",chardata"`
}

func (node xmlNode) walkRoutes() []xmlNode {
	var routes []xmlNode
	var walk func(current xmlNode)
	walk = func(current xmlNode) {
		if current.XMLName.Local == "auto_topic_route" || current.XMLName.Local == "topic_route" {
			routes = append(routes, current)
		}
		for _, child := range current.Nodes {
			walk(child)
		}
	}
	walk(node)
	return routes
}

func (node xmlNode) attr(name string) string {
	for _, attr := range node.Attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func (node xmlNode) firstChildText(name string) string {
	var walk func(current xmlNode) string
	walk = func(current xmlNode) string {
		if current.XMLName.Local == name && strings.TrimSpace(current.Text) != "" {
			return strings.TrimSpace(current.Text)
		}
		for _, child := range current.Nodes {
			if value := walk(child); value != "" {
				return value
			}
		}
		return ""
	}
	return walk(node)
}

func routeTopicFilter(routeElement xmlNode) string {
	if topic := routeElement.firstChildText("topic_name"); topic != "" {
		return topic
	}
	if filter := routeElement.firstChildText("allow_topic_name_filter"); filter != "" {
		return filter
	}
	return "*"
}
