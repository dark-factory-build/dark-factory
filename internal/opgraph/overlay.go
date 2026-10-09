package opgraph

import (
	"slices"
	"sort"
	"strings"
)

// Status is a node's or edge's runtime reading. Observation says how well we
// can see it; State says what it is doing, and is "unknown" whenever the
// observation cannot support a claim. Static evidence never sets State.
type Status struct {
	Observation string  `json:"observation"` // observed | quiet | partial | stale | unobserved | opaque
	State       string  `json:"state"`       // active | degraded | failing | idle | unknown
	Rate        float64 `json:"rate,omitempty"`
	ErrorRate   float64 `json:"error_rate,omitempty"`
	LatencyP95  float64 `json:"latency_p95_ms,omitempty"`
	LastSeen    int64   `json:"last_seen,omitempty"`
	// DeployedAt is the last deploy observed for this unit; a changeover,
	// not traffic.
	DeployedAt int64    `json:"deployed_at,omitempty"`
	Sources    []string `json:"sources,omitempty"`
	count      uint64
	errors     uint64
}

type Summary struct {
	Components   int `json:"components"`
	Inferred     int `json:"inferred"`
	Observed     int `json:"observed"`
	Quiet        int `json:"quiet"`
	Partial      int `json:"partial"`
	Stale        int `json:"stale"`
	Unobserved   int `json:"unobserved"`
	Opaque       int `json:"opaque"`
	RuntimeOnly  int `json:"runtime_only"`
	Contradicted int `json:"contradicted"`
}

type Live struct {
	Graph   Graph
	Nodes   map[string]*Status
	Edges   map[[3]string]*Status
	Summary Summary
}

const maxUnknownPerUnit = 32

// Overlay binds runtime evidence to the static graph and derives coverage
// and state. aliases maps platform names (Worker scripts, Vercel projects,
// OTel service names) to a unit's service.name.
func Overlay(system string, static Graph, observations []Observation, coverage []Coverage, aliases map[string]string, now, window int64) Live {
	builder := NewBuilder(system)
	for index := range static.Nodes {
		node := static.Nodes[index]
		node.Evidence = append([]Evidence{}, node.Evidence...)
		node.Selectors = copyMap(node.Selectors)
		builder.nodes[node.ID] = &node
	}
	for _, edge := range static.Edges {
		copied := edge
		copied.Evidence = append([]Evidence{}, edge.Evidence...)
		builder.edges[[3]string{edge.From, edge.To, string(edge.Kind)}] = &copied
	}
	run := overlay{builder: builder, units: map[string]string{}, aliases: aliases, nodes: map[string]*Status{}, edges: map[[3]string]*Status{},
		unknown: map[string]int{}, now: now, window: window}
	for _, node := range builder.nodes {
		if name := node.Selectors["service.name"]; node.Kind == Processor && name != "" {
			// Two units answering to one name cannot be told apart: neither binds.
			if _, held := run.units[name]; held {
				run.units[name] = ""
			} else {
				run.units[name] = node.ID
			}
		}
	}
	for _, item := range observations {
		run.observe(item)
	}
	current, expired := map[string][]Coverage{}, map[string]bool{}
	for _, item := range coverage {
		unit := run.unit(item.Unit)
		if unit == "" {
			continue
		}
		if item.AsOf+item.TTL >= now {
			current[unit] = append(current[unit], item)
		} else {
			expired[unit] = true
		}
	}
	graph, _ := builder.Graph()
	live := Live{Graph: graph, Nodes: map[string]*Status{}, Edges: map[[3]string]*Status{}}
	byID := map[string]*Node{}
	for index := range graph.Nodes {
		byID[graph.Nodes[index].ID] = &graph.Nodes[index]
	}
	users := map[string][]string{}
	for _, edge := range graph.Edges {
		if edge.Kind == Uses || edge.Kind == Publishes || edge.Kind == Calls {
			users[edge.To] = append(users[edge.To], edge.From)
		}
	}
	for index := range graph.Nodes {
		node := &graph.Nodes[index]
		status := run.status(node.ID)
		staticEvidence := false
		for _, item := range node.Evidence {
			staticEvidence = staticEvidence || item.Origin == "static"
		}
		switch {
		case !staticEvidence:
			status.Observation = "stale"
			if status.Rate > 0 {
				status.Observation = "observed"
			}
		case node.Kind == External:
			status.Observation = "opaque"
			if status.count == 0 && silentHost(run.builder, node, users[node.ID], current) {
				status.Observation = "quiet"
			}
		case node.Unit == "" && node.Kind != Processor:
			status.Observation = sharedObservation(node, users[node.ID], current, status)
		default:
			unit := node.Unit
			if node.Kind == Processor {
				unit = node.ID
			}
			status.Observation = unitObservation(node, current[unit], expired[unit], status)
		}
		live.Nodes[node.ID] = status
	}
	// A processor is partial when it is seen but some of its machinery is not.
	for index := range graph.Nodes {
		node := &graph.Nodes[index]
		if node.Unit == "" || node.Kind == External {
			continue
		}
		parent := live.Nodes[node.Unit]
		if child := live.Nodes[node.ID].Observation; parent != nil && (parent.Observation == "observed" || parent.Observation == "quiet") && (child == "partial" || child == "unobserved") {
			parent.Observation = "partial"
		}
	}
	for _, status := range live.Nodes {
		status.State = state(status)
	}
	for _, edge := range graph.Edges {
		key := [3]string{edge.From, edge.To, string(edge.Kind)}
		status := run.edgeStatus(key)
		switch {
		case edge.Kind == Handles || edge.Kind == Runs || edge.Kind == Consumes:
			// The node's own reading is the flow through this edge.
			end := edge.From
			if edge.Kind == Runs {
				end = edge.To
			}
			copied := *live.Nodes[end]
			status = &copied
		case status.count > 0:
			status.Observation = "observed"
		case hasPeers(current[edge.From]), live.Nodes[edge.To].Observation == "quiet" && byID[edge.To].Kind == External:
			status.Observation = "quiet"
		case expired[edge.From]:
			status.Observation = "stale"
		default:
			status.Observation = "unobserved"
		}
		status.State = state(status)
		live.Edges[key] = status
	}
	for _, node := range graph.Nodes {
		status := live.Nodes[node.ID]
		live.Summary.Components++
		switch State(node.Evidence) {
		case "runtime":
			live.Summary.RuntimeOnly++
		case "contradicted":
			live.Summary.Contradicted++
		}
		if State(node.Evidence) != "runtime" {
			live.Summary.Inferred++
		}
		switch status.Observation {
		case "observed":
			live.Summary.Observed++
		case "quiet":
			live.Summary.Quiet++
		case "partial":
			live.Summary.Partial++
		case "stale":
			live.Summary.Stale++
		case "unobserved":
			live.Summary.Unobserved++
		case "opaque":
			live.Summary.Opaque++
		}
	}
	live.Graph = graph
	return live
}

func unitObservation(node *Node, current []Coverage, expired bool, status *Status) string {
	// Traffic bound in the window is an observation whatever the coverage says.
	if status.count > 0 {
		return "observed"
	}
	for _, item := range current {
		if node.Kind == Processor || covers(item, node) {
			if status.count > 0 {
				return "observed"
			}
			// Silence is only claimed for what the source has been seen to
			// report: a static guess it never matched (a route prefix the
			// code hid, a timer slower than any window we hold) stays partial.
			if node.Kind != Processor && !runtimeSeen(node) || node.Trigger == "timer" {
				return "partial"
			}
			return "quiet"
		}
	}
	switch {
	case len(current) > 0:
		return "partial"
	case expired:
		return "stale"
	}
	return "unobserved"
}

// silentHost: an exact outside host every caller of which is a current source
// that reports server.address, so a call would have been seen. There is no
// guessed prefix to be wrong about, unlike a route.
func silentHost(builder *Builder, node *Node, callers []string, current map[string][]Coverage) bool {
	if node.Selectors["server.address"] == "" || len(callers) == 0 {
		return false
	}
	for _, caller := range callers {
		if from := builder.nodes[caller]; from != nil && from.Unit != "" {
			caller = from.Unit
		}
		reports := false
		for _, item := range current[caller] {
			reports = reports || slices.Contains(item.Keys, "server.address")
		}
		if !reports {
			return false
		}
	}
	return true
}

// NotConnected redraws an outside host as unobserved: the factory knows no
// integration is configured, so there is nothing to observe, not a silence.
func (live *Live) NotConnected(host string) {
	moved := map[string]bool{}
	for _, node := range live.Graph.Nodes {
		status := live.Nodes[node.ID]
		if node.Kind != External || node.Selectors["server.address"] != host || status == nil || status.count > 0 {
			continue
		}
		switch status.Observation {
		case "opaque":
			live.Summary.Opaque--
		case "quiet":
			live.Summary.Quiet--
		default:
			continue
		}
		live.Summary.Unobserved++
		status.Observation, status.State, moved[node.ID] = "unobserved", "unknown", true
	}
	for key, status := range live.Edges {
		if moved[key[1]] && status.count == 0 {
			status.Observation, status.State = "unobserved", "unknown"
		}
	}
}

func runtimeSeen(node *Node) bool {
	for _, item := range node.Evidence {
		if item.Origin == "runtime" {
			return true
		}
	}
	return false
}

func sharedObservation(node *Node, users []string, current map[string][]Coverage, status *Status) string {
	if status.count > 0 {
		return "observed"
	}
	seen := false
	for _, user := range users {
		for _, item := range current[user] {
			if item.Peers || covers(item, node) {
				return "quiet"
			}
			seen = true
		}
	}
	if seen {
		return "partial"
	}
	return "unobserved"
}

// covers: the source can report every selector the node binds on.
func covers(item Coverage, node *Node) bool {
	keys := map[string]bool{}
	for _, key := range item.Keys {
		keys[key] = true
	}
	for key := range node.Selectors {
		if !keys[key] && !(key == "http.route" && keys["url.path"]) {
			return false
		}
	}
	return len(node.Selectors) > 0
}

func hasPeers(items []Coverage) bool {
	for _, item := range items {
		if item.Peers {
			return true
		}
	}
	return false
}

func state(status *Status) string {
	switch status.Observation {
	case "quiet":
		return "idle"
	case "observed", "partial", "opaque":
		if status.count == 0 {
			return "unknown"
		}
		switch ratio := float64(status.errors) / float64(status.count); {
		case ratio >= 0.5:
			return "failing"
		case ratio >= 0.05:
			return "degraded"
		}
		return "active"
	}
	return "unknown"
}

type overlay struct {
	builder     *Builder
	units       map[string]string // service.name -> unit id
	aliases     map[string]string
	nodes       map[string]*Status
	edges       map[[3]string]*Status
	unknown     map[string]int
	now, window int64
}

func (run *overlay) unit(name string) string {
	if alias, ok := run.aliases[name]; ok {
		name = alias
	}
	return run.units[name]
}

func (run *overlay) status(id string) *Status {
	if run.nodes[id] == nil {
		run.nodes[id] = &Status{}
	}
	return run.nodes[id]
}

func (run *overlay) edgeStatus(key [3]string) *Status {
	if run.edges[key] == nil {
		run.edges[key] = &Status{}
	}
	return run.edges[key]
}

func (run *overlay) count(status *Status, item Observation) {
	if item.End > status.LastSeen {
		status.LastSeen = item.End
	}
	if !contains(status.Sources, item.Source) {
		status.Sources = append(status.Sources, item.Source)
		sort.Strings(status.Sources)
	}
	if item.End <= run.now-run.window {
		return
	}
	status.count += item.Count
	status.errors += item.Errors
	status.Rate = float64(status.count) / (float64(run.window) / float64(bucket))
	status.ErrorRate = float64(status.errors) / float64(max(status.count, 1))
	status.LatencyP95 = max(status.LatencyP95, item.LatencyP95)
}

func (run *overlay) observe(item Observation) {
	unit := run.unit(item.Attributes["service.name"])
	if item.Kind == "deploy" {
		if unit != "" {
			status := run.status(unit)
			status.DeployedAt = max(status.DeployedAt, item.End)
		}
		return
	}
	runtime := func(confidence string) Evidence {
		return Evidence{Origin: "runtime", Source: item.Source, Detail: item.Environment, Confidence: confidence}
	}
	if item.Kind == "client" || item.Kind == "producer" {
		if unit == "" || len(item.Peer) == 0 {
			return
		}
		target := run.bind(item.Peer, "")
		if target == nil {
			target = run.reach(item.Peer)
		}
		if id := run.unit(item.Peer["process.executable.name"]); target == nil && id != "" {
			target = run.builder.Lookup(id) // a launch of one of this system's units
		}
		if target == nil {
			target = run.runtimeParty(item.Peer)
		}
		if target == nil {
			return
		}
		target.Add(runtime(Inferred), nil)
		kind := Calls
		switch target.Kind {
		case Store:
			kind = Uses
		case Queue:
			kind = Publishes
		case Processor:
			// A call to a unit lands on that unit.
		}
		run.builder.Edge(unit, target.ID, kind, runtime(Inferred))
		run.count(run.edgeStatus([3]string{unit, target.ID, string(kind)}), item)
		if target.Kind == External || target.Kind == Store || target.Kind == Queue {
			run.count(run.status(target.ID), item)
		}
		return
	}
	confidence := Declared
	node := run.bind(item.Attributes, unit)
	if node == nil && unit != "" && !salient(item.Attributes) {
		node = run.builder.Lookup(unit)
	}
	if node == nil && unit == "" {
		confidence = Inferred
		node = run.bind(item.Attributes, "")
	}
	if node == nil {
		// Contradiction needs an exact, specific route with one other owner:
		// generic routes (/health, catch-alls) prove nothing.
		if route := item.Attributes["http.route"]; route != "" && unit != "" && staticSegments(route) >= 2 && !strings.Contains(normaliseRoute(route), "{*}") {
			if other := run.bind(map[string]string{"http.route": route, "http.request.method": item.Attributes["http.request.method"]}, "*"); other != nil {
				other.Add(Evidence{Origin: "runtime", Source: item.Source, Detail: "observed served by " + run.builder.Lookup(unit).Label, Confidence: Contradicted}, nil)
			}
		}
		node = run.unknownNode(unit, item.Attributes)
	}
	if node == nil {
		return
	}
	node.Add(runtime(confidence), nil)
	run.count(run.status(node.ID), item)
	if unit != "" && node.ID != unit {
		run.count(run.status(unit), item)
	}
}

// bind finds the node whose selectors the attributes satisfy; within one
// unit (unit != ""), among shared parties (unit == ""), or among other
// units' nodes ("*"). The most specific match wins; a tie binds nothing.
func (run *overlay) bind(attributes map[string]string, unit string) *Node {
	var best *Node
	bestScore, tied := 0.0, false
	for _, node := range run.builder.nodes {
		if node.Kind == Processor || len(node.Selectors) == 0 {
			continue
		}
		switch {
		case unit == "*" && node.Unit == "":
			continue
		case unit != "*" && node.Unit != unit:
			continue
		}
		// Work naming a route, destination or function belongs to a node
		// naming one, never to the listener it arrived on: unmatched, it stays
		// visible as unknown. Methods on a socket whose code names none are
		// that socket's work.
		rpcOnly := attributes["rpc.method"] != "" && attributes["http.route"]+attributes["url.path"]+attributes["messaging.destination.name"]+attributes["code.function.name"] == ""
		if salient(attributes) && !salient(node.Selectors) && !rpcOnly {
			continue
		}
		score, ok := match(node.Selectors, attributes)
		if !ok {
			continue
		}
		switch {
		case score > bestScore:
			best, bestScore, tied = node, score, false
		case score == bestScore:
			tied = true
		}
	}
	if tied {
		return nil
	}
	return best
}

// match scores how specifically attributes satisfy every selector; static
// route segments outrank parameters.
func match(selectors, attributes map[string]string) (float64, bool) {
	score := 0.0
	for key, want := range selectors {
		switch key {
		case "http.route":
			route := attributes["http.route"]
			if route != "" {
				if normaliseRoute(route) != normaliseRoute(want) {
					return 0, false
				}
			} else if !routeMatches(want, attributes["url.path"]) {
				return 0, false
			}
			score += 1 + 0.01*float64(staticSegments(want))
		case "http.request.method":
			got := strings.ToUpper(attributes[key])
			if got == "HEAD" {
				got = "GET"
			}
			if got != "" && got != strings.ToUpper(want) {
				return 0, false
			}
			score++
		case "network.transport":
			if got := attributes[key]; got != "" && got != want {
				return 0, false
			}
			score += 0.5
		default:
			if attributes[key] != want {
				return 0, false
			}
			score++
		}
	}
	return score, true
}

func routeMatches(template, concrete string) bool {
	if concrete == "" {
		return false
	}
	want := strings.Split(normaliseRoute(template), "/")
	got := strings.Split(normaliseRoute(concrete), "/")
	for index, segment := range want {
		if segment == "{*}" {
			return true
		}
		if index >= len(got) || segment != "{}" && segment != got[index] {
			return false
		}
	}
	return len(got) == len(want)
}

func staticSegments(route string) int {
	count := 0
	for _, segment := range strings.Split(normaliseRoute(route), "/") {
		if segment != "" && !strings.HasPrefix(segment, "{") {
			count++
		}
	}
	return count
}

func salient(attributes map[string]string) bool {
	for _, key := range []string{"http.route", "url.path", "rpc.method", "messaging.destination.name", "code.function.name"} {
		if attributes[key] != "" {
			return true
		}
	}
	return false
}

// unknownNode records runtime activity no static node explains, keyed
// coarsely and capped per unit so scanners and concrete ids cannot flood it.
func (run *overlay) unknownNode(unit string, attributes map[string]string) *Node {
	// Runtime evidence is factory-wide: a service no unit of this project
	// answers to belongs to some other system, not to this one's quarantine.
	if unit == "" {
		return nil
	}
	label, selectors := "", map[string]string{}
	switch {
	case attributes["rpc.method"] != "":
		label = attributes["rpc.method"]
		selectors["rpc.method"] = label
	case attributes["http.route"]+attributes["url.path"] != "":
		route := attributes["http.route"]
		if route == "" {
			route = attributes["url.path"]
		}
		first := strings.SplitN(strings.TrimPrefix(normaliseRoute(route), "/"), "/", 2)[0]
		label = strings.TrimSpace(attributes["http.request.method"] + " /" + first)
		selectors["http.route"] = "/" + first + "/{*}"
		if first == "" {
			selectors["http.route"] = "/"
		}
	case attributes["messaging.destination.name"] != "":
		label = attributes["messaging.destination.name"]
		selectors["messaging.destination.name"] = label
	case attributes["code.function.name"] != "":
		label = attributes["code.function.name"]
		selectors["code.function.name"] = label
	default:
		return nil
	}
	key := "runtime:" + label
	if run.builder.Lookup(ID(run.builder.system, unit, key)) == nil {
		if run.unknown[unit] >= maxUnknownPerUnit {
			key, label, selectors = "runtime:other", "other unmapped activity", nil
		} else {
			run.unknown[unit]++
		}
	}
	node := run.builder.Node(Unknown, unit, key, label)
	for name, value := range selectors {
		node.Select(name, value)
	}
	run.builder.Edge(node.ID, unit, Handles, Evidence{Origin: "runtime", Source: "correlation", Detail: "unmapped", Confidence: Inferred})
	return node
}

// reach finds the ingress a peer address names: a declared host or a
// loopback listener. Only a unique match binds.
func (run *overlay) reach(peer map[string]string) *Node {
	host, port := strings.ToLower(peer["server.address"]), peer["server.port"]
	if host == "" {
		return nil
	}
	var found *Node
	for _, node := range run.builder.nodes {
		address := node.Selectors["server.address"]
		if node.Kind != Ingress || address == "" || !hostMatches(address, host) && !(loopback(host) && loopback(address)) || port != "" && node.Selectors["server.port"] != "" && node.Selectors["server.port"] != port {
			continue
		}
		if found != nil {
			return nil
		}
		found = node
	}
	return found
}

// runtimeParty is an outbound peer no static evidence named.
func (run *overlay) runtimeParty(peer map[string]string) *Node {
	for _, pair := range [][2]string{{"server.address", "host:"}, {"process.executable.name", "process:"}, {"peer.service", "service:"}} {
		if value := peer[pair[0]]; value != "" {
			node := run.builder.Node(External, "", pair[1]+value, value)
			node.Select(pair[0], value)
			return node
		}
	}
	return nil
}

func copyMap(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
