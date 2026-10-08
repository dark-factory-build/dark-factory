package opgraph

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
)

// PublicWorld is the safe projection of one factory. It is an allowlist:
// kinds, shape, coverage, bucketed activity and where workers are, never
// labels, paths, selectors, hosts, routes, versions, people or task text.
type PublicWorld struct {
	GeneratedAt int64          `json:"generated_at"`
	Summary     Summary        `json:"summary"`
	Nodes       []PublicNode   `json:"nodes"`
	Edges       []PublicEdge   `json:"edges"`
	Workers     []PublicWorker `json:"workers"`
	// Crates is the outbound work line: never a title, number or branch.
	Crates []PublicCrate `json:"crates"`
	// Ledger is present only when a repository is public on GitHub, and then
	// carries only that repository's public work.
	Ledger *PublicLedger `json:"ledger,omitempty"`
}

// Crate is one pull request on the outbound line: Key names it privately,
// Station is REVIEW 0, CHECKS 1, MERGE QUEUE 2 or SHIPPED 3.
type Crate struct {
	Key     string
	Station int
	Fault   bool
}

type PublicCrate struct {
	ID      string `json:"id"`
	Station int    `json:"station"`
	Fault   bool   `json:"fault,omitempty"`
}

// PublicLedger is what is already public on GitHub about the factory's public
// repositories, as its own records saw it. Times are UTC RFC 3339.
type PublicLedger struct {
	// WindowDays is the rolling window Merged and MergedCount cover.
	WindowDays  int          `json:"window_days"`
	Open        []LedgerPull `json:"open"`
	OpenCount   int          `json:"open_count"`
	Issues      []LedgerPull `json:"issues"`
	IssueCount  int          `json:"issue_count"`
	Merged      []LedgerPull `json:"merged"` // newest first
	MergedCount int          `json:"merged_count"`
	Releases    []LedgerTag  `json:"releases"` // newest first
	Clock       []LedgerDay  `json:"clock"`    // oldest first
}

type LedgerPull struct {
	Number   uint64 `json:"number"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	MergedAt string `json:"merged_at,omitempty"`
}

type LedgerTag struct {
	Tag         string `json:"tag"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease,omitempty"`
}

// LedgerDay is one UTC day of recorded merges, hour by hour.
type LedgerDay struct {
	Date  string  `json:"date"`
	Hours [24]int `json:"hours"`
}

type PublicNode struct {
	ID          string `json:"id"`
	Kind        Kind   `json:"kind"`
	Label       string `json:"label"`
	Unit        string `json:"unit,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
	Evidence    string `json:"evidence"`
	Observation string `json:"observation"`
	State       string `json:"state"`
	Activity    string `json:"activity"` // none | low | medium | high
	// Deployed says a changeover was observed in the last day; never when.
	Deployed bool `json:"deployed,omitempty"`
}

type PublicEdge struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Kind        string `json:"kind"`
	Evidence    string `json:"evidence"`
	Observation string `json:"observation"`
	State       string `json:"state"`
	Activity    string `json:"activity"`
}

// PublicWorker is one agent: what it is doing and which hall it is in.
type PublicWorker struct {
	Activity string `json:"activity"` // busy | waiting | needs-you | idle
	Unit     string `json:"unit,omitempty"`
}

// Worker is a private worker reading the projection reduces to PublicWorker.
type Worker struct {
	Activity string
	Unit     string // operational node ID of the unit it works in, if known
}

// Public names say what a station is in plain words: a unit by its runtime,
// an entrance by its trigger, a job by whether CI runs it.
var (
	publicNames  = map[Kind]string{Processor: "Unit", Ingress: "Entrance", Job: "Loop", Queue: "Queue", Store: "Store", External: "Outside service", Unknown: "Unknown"}
	unitNames    = map[string]string{"browser": "Web app", "server": "Web server", "worker": "Edge function", "process": "Service", "cli": "Tool", "ci": "CI pipeline"}
	triggerNames = map[string]string{"timer": "Timer", "message": "Inbox"}
)

func publicName(node Node, runtimes map[string]string) string {
	switch {
	case node.Kind == Processor && unitNames[node.Runtime] != "":
		return unitNames[node.Runtime]
	case node.Kind == Ingress && triggerNames[node.Trigger] != "":
		return triggerNames[node.Trigger]
	case node.Kind == Job && runtimes[node.Unit] == "ci":
		return "Check"
	}
	return publicNames[node.Kind]
}

// Public projects the live graph with an operator secret: IDs are keyed
// hashes, so they are stable for this factory and meaningless elsewhere;
// labels are ordinals in hashed-ID order, so they do not shift as other
// nodes come and go. Time is bucketed to five minutes.
func Public(live Live, secret []byte, workers []Worker, crates []Crate, now int64) PublicWorld {
	id := func(raw string) string {
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(raw))
		return hex.EncodeToString(mac.Sum(nil)[:16])
	}
	runtimes := map[string]string{}
	for _, node := range live.Graph.Nodes {
		runtimes[node.ID] = node.Runtime
	}
	nodes := make([]PublicNode, 0, len(live.Graph.Nodes))
	for _, node := range live.Graph.Nodes {
		status := live.Nodes[node.ID]
		item := PublicNode{ID: id(node.ID), Kind: node.Kind, Label: publicName(node, runtimes), Runtime: node.Runtime, Trigger: node.Trigger, Evidence: State(node.Evidence),
			Observation: status.Observation, State: status.State, Activity: activity(status), Deployed: status.DeployedAt > now-24*60*60_000}
		if node.Unit != "" {
			item.Unit = id(node.Unit)
		}
		nodes = append(nodes, item)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	ordinal := map[string]int{}
	for index := range nodes {
		ordinal[nodes[index].Label]++
		nodes[index].Label += " " + strconv.Itoa(ordinal[nodes[index].Label])
	}
	edges := []PublicEdge{}
	for _, edge := range live.Graph.Edges {
		status := live.Edges[[3]string{edge.From, edge.To, string(edge.Kind)}]
		edges = append(edges, PublicEdge{From: id(edge.From), To: id(edge.To), Kind: string(edge.Kind), Evidence: State(edge.Evidence), Observation: status.Observation, State: status.State, Activity: activity(status)})
	}
	sort.Slice(edges, func(i, j int) bool {
		return edges[i].From+edges[i].To+edges[i].Kind < edges[j].From+edges[j].To+edges[j].Kind
	})
	public := []PublicWorker{}
	for _, worker := range workers {
		item := PublicWorker{Activity: worker.Activity}
		if worker.Unit != "" {
			item.Unit = id(worker.Unit)
		}
		public = append(public, item)
	}
	sort.Slice(public, func(i, j int) bool { return public[i].Unit+public[i].Activity < public[j].Unit+public[j].Activity })
	line := []PublicCrate{}
	for _, crate := range crates {
		line = append(line, PublicCrate{ID: id("crate:" + crate.Key), Station: crate.Station, Fault: crate.Fault})
	}
	sort.Slice(line, func(i, j int) bool { return line[i].ID < line[j].ID })
	return PublicWorld{GeneratedAt: now - now%(5*60_000), Summary: live.Summary, Nodes: nodes, Edges: edges, Workers: public, Crates: line}
}

// activity buckets a rate by powers of eight per hour, so exact traffic and
// its timing never leave the machine.
func activity(status *Status) string {
	perHour := status.Rate * 60
	switch {
	case status.State == "unknown" || status.State == "idle" || perHour < 1:
		return "none"
	case math.Log2(perHour) < 6:
		return "low"
	case math.Log2(perHour) < 12:
		return "medium"
	}
	return "high"
}

// Locate finds the unit whose code area holds a repository path: the most
// specific module wins, and a running unit is preferred to a tool.
func Locate(graph Graph, repository, file string) string {
	best, bestLength, bestRank := "", -1, 99
	rank := map[string]int{"process": 0, "server": 1, "worker": 2, "browser": 3, "cli": 4}
	for _, node := range graph.Nodes {
		if node.Kind != Processor {
			continue
		}
		for _, module := range node.Modules {
			if module.Repository != repository || !within(module.Path, path.Clean(file)) {
				continue
			}
			length := len(module.Path)
			if module.Path == "." {
				length = 0
			}
			order, ok := rank[node.Runtime]
			if !ok {
				order = 5
			}
			if length > bestLength || length == bestLength && (order < bestRank || order == bestRank && strings.Compare(node.ID, best) < 0) {
				best, bestLength, bestRank = node.ID, length, order
			}
		}
	}
	return best
}
