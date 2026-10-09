// Package opgraph is the Operational Graph: a language-neutral model of what a
// software system could do (static evidence) and what it was seen doing
// (runtime evidence). It holds no rendering concepts.
package opgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Bounds keep one served graph inside the browser frame.
const (
	MaxNodes   = 4096
	MaxEdges   = 4096
	maxSources = 8
	maxModules = 128
	maxText    = 256
)

var ErrBounds = errors.New("operational graph bounds exceeded")

type Kind string

const (
	Processor Kind = "processor"
	Ingress   Kind = "ingress"
	Job       Kind = "job"
	Queue     Kind = "queue"
	Store     Kind = "store"
	External  Kind = "external"
	Unknown   Kind = "unknown"
)

type EdgeKind string

const (
	Handles   EdgeKind = "handles"   // ingress -> processor
	Calls     EdgeKind = "calls"     // processor -> ingress | external
	Uses      EdgeKind = "uses"      // processor -> store
	Publishes EdgeKind = "publishes" // processor -> queue
	Consumes  EdgeKind = "consumes"  // queue -> processor
	Runs      EdgeKind = "runs"      // processor -> job
)

// These typed sets, Kind and EdgeKind are the graph contract; contract_test.go holds the console to them.
type Placement string     // where a processor runs
type Trigger string       // what starts an ingress
type EvidenceState string // derived by State
type Visibility string    // how well a reading sees its node or edge
type Activity string      // what a reading claims it is doing
type Origin string

const RuntimeProcess, RuntimeCLI, RuntimeWorker, RuntimeServer, RuntimeBrowser, RuntimeCI Placement = "process", "cli", "worker", "server", "browser", "ci"
const TriggerRequest, TriggerTimer, TriggerMessage Trigger = "request", "timer", "message"
const EvidenceStatic, EvidenceRuntime, EvidenceBoth, EvidenceUncertain, EvidenceContradicted EvidenceState = "static", "runtime", "both", "uncertain", "contradicted"
const Observed, Quiet, Partial, Stale, Unobserved, Opaque Visibility = "observed", "quiet", "partial", "stale", "unobserved", "opaque"
const Active, Degraded, Failing, Idle, StateUnknown Activity = "active", "degraded", "failing", "idle", "unknown"
const OriginStatic, OriginRuntime Origin = "static", "runtime"

// Confidence grades one piece of evidence.
const (
	Declared     = "declared"     // a declaration the framework itself executes
	Inferred     = "inferred"     // a recognised code pattern
	Heuristic    = "heuristic"    // a literal or a name
	Contradicted = "contradicted" // runtime evidence disagreeing with static evidence
)

type Evidence struct {
	Origin     Origin `json:"origin"`
	Source     string `json:"source"` // extractor or adapter
	Detail     string `json:"detail,omitempty"`
	Confidence string `json:"confidence"`
}

type Location struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Line       int    `json:"line,omitempty"`
}

type Node struct {
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	Label string `json:"label"`
	// Unit is the processor node this node runs in; empty for processors and
	// for system-wide parties (externals, shared stores).
	Unit    string    `json:"unit,omitempty"`
	Runtime Placement `json:"runtime,omitempty"`
	// Deployed marks a processor a deployment declaration names.
	Deployed bool       `json:"deployed,omitempty"`
	Trigger  Trigger    `json:"trigger,omitempty"`
	Sources  []Location `json:"sources,omitempty"`
	// Modules are the code areas a processor runs: its package or module
	// directories. Ownership stays inspectable without deciding placement.
	Modules   []Location        `json:"modules,omitempty"`
	Selectors map[string]string `json:"selectors,omitempty"`
	Evidence  []Evidence        `json:"evidence"`
}

type Edge struct {
	From     string     `json:"from"`
	To       string     `json:"to"`
	Kind     EdgeKind   `json:"kind"`
	Evidence []Evidence `json:"evidence"`
}

type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// State derives the evidence state: static, runtime, both, uncertain or
// contradicted. It is never stored.
func State(evidence []Evidence) EvidenceState {
	static, runtime, certain := false, false, false
	for _, item := range evidence {
		if item.Confidence == Contradicted {
			return EvidenceContradicted
		}
		static = static || item.Origin == OriginStatic
		runtime = runtime || item.Origin == OriginRuntime
		certain = certain || item.Confidence != Heuristic
	}
	switch {
	case static && runtime:
		return EvidenceBoth
	case runtime:
		return EvidenceRuntime
	case !certain:
		return EvidenceUncertain
	default:
		return EvidenceStatic
	}
}

// ID is stable across layout, revisions, labels, kind and activity: only
// the system, owning unit and the node's own namespaced key decide it.
func ID(system, unit, key string) string {
	hash := sha256.New()
	for _, part := range []string{"dark-factory/opgraph/node", system, unit, key} {
		hash.Write([]byte(strconv.Itoa(len(part)) + ":" + part))
	}
	return hex.EncodeToString(hash.Sum(nil)[:16])
}

// Builder merges evidence: the same key seen twice is one node.
type Builder struct {
	system string
	nodes  map[string]*Node
	edges  map[[3]string]*Edge
	err    error
}

func NewBuilder(system string) *Builder {
	return &Builder{system: system, nodes: map[string]*Node{}, edges: map[[3]string]*Edge{}}
}

// Node returns the node for (unit, key), creating it with kind and label.
func (builder *Builder) Node(kind Kind, unit, key, label string) *Node {
	id := ID(builder.system, unit, key)
	if node := builder.nodes[id]; node != nil {
		return node
	}
	if len(builder.nodes) == MaxNodes {
		builder.err = ErrBounds
		return &Node{Selectors: map[string]string{}} // absorbs writes; Graph reports the bound
	}
	node := &Node{ID: id, Kind: kind, Unit: unit, Label: clip(label), Selectors: map[string]string{}}
	builder.nodes[id] = node
	return node
}

func (builder *Builder) Lookup(id string) *Node { return builder.nodes[id] }

func (builder *Builder) Edge(from, to string, kind EdgeKind, evidence Evidence) {
	if from == "" || to == "" || from == to {
		return
	}
	key := [3]string{from, to, string(kind)}
	edge := builder.edges[key]
	if edge == nil {
		if len(builder.edges) == MaxEdges {
			builder.err = ErrBounds
			return
		}
		edge = &Edge{From: from, To: to, Kind: kind}
		builder.edges[key] = edge
	}
	edge.Evidence = addEvidence(edge.Evidence, evidence)
}

func (node *Node) Add(evidence Evidence, at *Location) *Node {
	node.Evidence = addEvidence(node.Evidence, evidence)
	if at != nil && at.Path != "" && len(node.Sources) < maxSources {
		for _, held := range node.Sources {
			if held == *at {
				return node
			}
		}
		node.Sources = append(node.Sources, *at)
	}
	return node
}

func (node *Node) Select(key, value string) *Node {
	if value != "" {
		node.Selectors[key] = clip(value)
	}
	return node
}

func addEvidence(held []Evidence, item Evidence) []Evidence {
	item.Detail = clip(item.Detail)
	for _, existing := range held {
		if existing == item {
			return held
		}
	}
	if len(held) >= 16 {
		return held
	}
	return append(held, item)
}

// Graph returns the merged graph in a deterministic order.
func (builder *Builder) Graph() (Graph, error) {
	result := Graph{Nodes: make([]Node, 0, len(builder.nodes)), Edges: make([]Edge, 0, len(builder.edges))}
	for _, node := range builder.nodes {
		sort.Slice(node.Sources, func(i, j int) bool {
			a, b := node.Sources[i], node.Sources[j]
			return a.Repository+a.Path < b.Repository+b.Path || a.Repository+a.Path == b.Repository+b.Path && a.Line < b.Line
		})
		if len(node.Selectors) == 0 {
			node.Selectors = nil
		}
		result.Nodes = append(result.Nodes, *node)
	}
	for _, edge := range builder.edges {
		if builder.nodes[edge.From] != nil && builder.nodes[edge.To] != nil {
			result.Edges = append(result.Edges, *edge)
		}
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].ID < result.Nodes[j].ID })
	sort.Slice(result.Edges, func(i, j int) bool {
		a, b := result.Edges[i], result.Edges[j]
		return a.From+a.To+string(a.Kind) < b.From+b.To+string(b.Kind)
	})
	return result, builder.err
}

func clip(value string) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	if len(value) > maxText {
		value = strings.ToValidUTF8(value[:maxText], "")
	}
	return value
}
