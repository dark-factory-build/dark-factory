package opgraph

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// PublicWorld is the safe projection of one factory. Nodes from repositories
// GitHub serves anonymously keep their real static labels, source paths and
// rates; everything else is shape under ordinal names and bucketed rates.
// Selectors, versions, people, task text and errors never appear.
type PublicWorld struct {
	GeneratedAt int64          `json:"generated_at"`
	Summary     Summary        `json:"summary"`
	Nodes       []PublicNode   `json:"nodes"`
	Edges       []PublicEdge   `json:"edges"`
	Workers     []PublicWorker `json:"workers"`
	// Crates is the outbound work line: number and title only for public repositories.
	Crates []PublicCrate `json:"crates"`
	// Ledger is present only when a repository is public on GitHub, and then
	// carries only that repository's public work.
	Ledger *PublicLedger `json:"ledger,omitempty"`
}

// Crate is one pull request on the outbound line: Key names it privately,
// Station is REVIEW 0, CHECKS 1, MERGE QUEUE 2 or SHIPPED 3.
// Repository is its lowercase owner/name; Number and Title are published
// only when that repository is named.
type Crate struct {
	Key        string
	Station    int
	Fault      bool
	Repository string
	Number     uint64
	Title      string
}

type PublicCrate struct {
	ID      string `json:"id"`
	Station int    `json:"station"`
	Fault   bool   `json:"fault,omitempty"`
	Number  uint64 `json:"number,omitempty"`
	Title   string `json:"title,omitempty"`
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
	ID          string        `json:"id"`
	Kind        Kind          `json:"kind"`
	Label       string        `json:"label"`
	Unit        string        `json:"unit,omitempty"`
	Runtime     Placement     `json:"runtime,omitempty"`
	Trigger     Trigger       `json:"trigger,omitempty"`
	Evidence    EvidenceState `json:"evidence"`
	Observation Visibility    `json:"observation"`
	State       Activity      `json:"state"`
	// RatePerHour is exact for a named node, else one of 0, 30, 600, 6000.
	RatePerHour float64  `json:"rate_per_hour"`
	Paths       []string `json:"paths,omitempty"` // named nodes only
	// Deployed says a changeover was observed in the last day; never when.
	Deployed bool `json:"deployed,omitempty"`
}

type PublicEdge struct {
	From        string        `json:"from"`
	To          string        `json:"to"`
	Kind        string        `json:"kind"`
	Evidence    EvidenceState `json:"evidence"`
	Observation Visibility    `json:"observation"`
	State       Activity      `json:"state"`
	RatePerHour float64       `json:"rate_per_hour"`
}

// PublicWorker is one agent: what it is doing and which unit it is at.
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
	unitNames    = map[Placement]string{RuntimeBrowser: "Web app", RuntimeServer: "Web server", RuntimeWorker: "Edge function", RuntimeProcess: "Service", RuntimeCLI: "Tool", RuntimeCI: "CI pipeline"}
	triggerNames = map[Trigger]string{TriggerTimer: "Timer", TriggerMessage: "Inbox"}
)

func publicName(node Node, runtimes map[string]Placement) string {
	switch {
	case node.Kind == Processor && unitNames[node.Runtime] != "":
		return unitNames[node.Runtime]
	case node.Kind == Ingress && triggerNames[node.Trigger] != "":
		return triggerNames[node.Trigger]
	case node.Kind == Job && runtimes[node.Unit] == RuntimeCI:
		return "Check"
	}
	return publicNames[node.Kind]
}

// Public projects the live graph with an operator secret: IDs are keyed
// hashes, so they are stable for this factory and meaningless elsewhere.
// named holds the repository IDs and lowercase owner/names GitHub serves
// anonymously. A node whose every source is in named keeps its label, paths
// and exact rate; any other node (private, or runtime-only with no source)
// gets an ordinal label, in hashed-ID order so it does not shift as other
// nodes come and go, and a bucketed rate. Time is bucketed to five minutes.
func Public(live Live, secret []byte, workers []Worker, crates []Crate, named map[string]bool, now int64) PublicWorld {
	id := func(raw string) string {
		mac := hmac.New(sha256.New, secret)
		mac.Write([]byte(raw))
		return hex.EncodeToString(mac.Sum(nil)[:16])
	}
	runtimes := map[string]Placement{}
	open := map[string]bool{} // raw node ID
	for _, node := range live.Graph.Nodes {
		runtimes[node.ID] = node.Runtime
		places := append(append([]Location{}, node.Sources...), node.Modules...)
		open[node.ID] = len(places) > 0
		for _, place := range places {
			open[node.ID] = open[node.ID] && named[place.Repository]
		}
	}
	nodes := make([]PublicNode, 0, len(live.Graph.Nodes))
	real := map[string]bool{} // hashed ID
	for _, node := range live.Graph.Nodes {
		status := live.Nodes[node.ID]
		item := PublicNode{ID: id(node.ID), Kind: node.Kind, Label: publicName(node, runtimes), Runtime: node.Runtime, Trigger: node.Trigger, Evidence: State(node.Evidence),
			Observation: status.Observation, State: status.State, RatePerHour: rate(status, open[node.ID]), Deployed: status.DeployedAt > now-24*60*60_000}
		if open[node.ID] {
			real[item.ID], item.Label = true, node.Label
			for _, source := range node.Sources {
				item.Paths = append(item.Paths, source.Path)
			}
		}
		if node.Unit != "" {
			item.Unit = id(node.Unit)
		}
		nodes = append(nodes, item)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	ordinal := map[string]int{}
	for index := range nodes {
		if !real[nodes[index].ID] {
			ordinal[nodes[index].Label]++
			nodes[index].Label += " " + strconv.Itoa(ordinal[nodes[index].Label])
		}
	}
	edges := []PublicEdge{}
	for _, edge := range live.Graph.Edges {
		status := live.Edges[[3]string{edge.From, edge.To, string(edge.Kind)}]
		edges = append(edges, PublicEdge{From: id(edge.From), To: id(edge.To), Kind: string(edge.Kind), Evidence: State(edge.Evidence), Observation: status.Observation, State: status.State, RatePerHour: rate(status, open[edge.From] && open[edge.To])})
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
		item := PublicCrate{ID: id("crate:" + crate.Key), Station: crate.Station, Fault: crate.Fault}
		if named[crate.Repository] {
			item.Number, item.Title = crate.Number, crate.Title
		}
		line = append(line, item)
	}
	sort.Slice(line, func(i, j int) bool { return line[i].ID < line[j].ID })
	return PublicWorld{GeneratedAt: now - now%(5*60_000), Summary: live.Summary, Nodes: nodes, Edges: edges, Workers: public, Crates: line}
}

// rate is the exact rate per hour for a named reading; otherwise it is
// bucketed to 0, 30, 600 or 6000 (powers of eight per hour), so exact
// traffic and its timing stay on the machine.
func rate(status *Status, exact bool) float64 {
	perHour := status.Rate * 60
	switch {
	case status.State == StateUnknown || status.State == Idle || perHour < 1:
		return 0
	case exact:
		return perHour
	case math.Log2(perHour) < 6:
		return 30
	case math.Log2(perHour) < 12:
		return 600
	}
	return 6000
}

// runtimeOrder is the unit a path prefers, as the console's GRAPH_RUNTIMES.
var runtimeOrder = []Placement{RuntimeProcess, RuntimeServer, RuntimeWorker, RuntimeBrowser, RuntimeCLI, RuntimeCI}

// Locate finds the unit whose code area holds a repository path: the most
// specific module wins, and a running unit is preferred to a tool.
func Locate(graph Graph, repository, file string) string {
	best, bestLength, bestRank := "", -1, 99
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
			order := slices.Index(runtimeOrder, node.Runtime)
			if length > bestLength || length == bestLength && (order < bestRank || order == bestRank && strings.Compare(node.ID, best) < 0) {
				best, bestLength, bestRank = node.ID, length, order
			}
		}
	}
	return best
}
