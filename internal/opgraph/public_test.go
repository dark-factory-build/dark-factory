package opgraph

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every private field carries a canary; none may reach the projection of a private repository.
func TestPublicProjectionIsAnAllowlist(t *testing.T) {
	canary := "CANARY"
	repositories := []Repository{{ID: canary + "repo", Name: canary + "name", Files: map[string][]byte{
		"go.mod": []byte("module example.com/" + canary + "\n"),
		"cmd/" + canary + "svc/main.go": []byte(`package main
import ("net/http"; "os/exec")
const ` + canary + `URL = "https://api.` + strings.ToLower(canary) + `secret.com/v1/token?key=` + canary + `"
func main() { http.HandleFunc("GET /` + canary + `/route", nil); http.Get(` + canary + `URL); exec.Command("` + canary + `tool") }
`)}}}
	graph, err := Infer(canary+"system", repositories)
	if err != nil || len(graph.Nodes) < 3 {
		t.Fatalf("fixture graph: %d nodes, %v", len(graph.Nodes), err)
	}
	observations := []Observation{{Source: "otlp", Kind: "server", Start: 0, End: 60_000, Count: 5000,
		Attributes: map[string]string{"service.name": canary + "svc", "http.route": "/" + canary + "/unmapped", "rpc.method": canary}}}
	live := Overlay(canary+"system", graph, observations, nil, nil, 60_000, 15*minute)
	var unit string
	for _, node := range live.Graph.Nodes {
		if node.Kind == Processor {
			unit = node.ID
		}
	}
	world := Public(live, []byte("secret"), []Worker{{Activity: "busy", Unit: unit}}, []Crate{{Key: canary + "/repo#7", Station: 2, Fault: true, Repository: canary, Number: 7, Title: canary}}, nil, 123_456_789)
	encoded, _ := json.Marshal(world)
	if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(canary)) || strings.Contains(string(encoded), unit) {
		t.Fatalf("private data leaked: %s", encoded)
	}
	if world.GeneratedAt%(5*60_000) != 0 || len(world.Workers) != 1 || world.Workers[0].Unit == "" || len(world.Crates) != 1 || world.Crates[0] != (PublicCrate{ID: world.Crates[0].ID, Station: 2, Fault: true}) || len(world.Crates[0].ID) != 32 {
		t.Fatalf("world = %+v", world)
	}
	again := Public(live, []byte("secret"), nil, nil, nil, 0)
	other := Public(live, []byte("another"), nil, nil, nil, 0)
	if again.Nodes[0].ID != world.Nodes[0].ID || other.Nodes[0].ID == world.Nodes[0].ID {
		t.Fatal("public identities are not stable per secret")
	}
	for _, node := range world.Nodes {
		if node.RatePerHour != 0 && node.Observation == "unobserved" {
			t.Fatalf("activity claimed without observation: %+v", node)
		}
	}
}

func TestLocatePrefersSpecificModulesAndRunningUnits(t *testing.T) {
	graph := Graph{Nodes: []Node{
		{ID: "tool", Kind: Processor, Runtime: "cli", Modules: []Location{{Repository: "r", Path: "internal/shared"}}},
		{ID: "daemon", Kind: Processor, Runtime: "process", Modules: []Location{{Repository: "r", Path: "internal/shared"}, {Repository: "r", Path: "internal/daemon"}}},
		{ID: "site", Kind: Processor, Runtime: "server", Modules: []Location{{Repository: "s", Path: "."}}},
	}}
	for _, item := range [][3]string{{"r", "internal/shared/a.go", "daemon"}, {"r", "internal/daemon/x/y.go", "daemon"}, {"s", "app/page.tsx", "site"}, {"r", "docs/readme.md", ""}} {
		if got := Locate(graph, item[0], item[1]); got != item[2] {
			t.Errorf("Locate(%s, %s) = %q, want %q", item[0], item[1], got, item[2])
		}
	}
}

// Public names come from what is already public (runtime, trigger, the unit's
// runtime) and count per name, so a CI check never reads as a background loop.
func TestPublicNamesSayWhatAStationIs(t *testing.T) {
	graph := Graph{Nodes: []Node{
		{ID: "ci", Kind: Processor, Runtime: "ci"}, {ID: "edge", Kind: Processor, Runtime: "worker"},
		{ID: "check", Kind: Job, Unit: "ci"}, {ID: "loop", Kind: Job, Unit: "edge"},
		{ID: "route", Kind: Ingress, Unit: "edge", Trigger: "request"}, {ID: "cron", Kind: Ingress, Unit: "edge", Trigger: "timer"},
		{ID: "api", Kind: External},
	}}
	got := map[string]bool{}
	for _, node := range Public(Overlay("s", graph, nil, nil, nil, 0, minute), []byte("k"), nil, nil, nil, 0).Nodes {
		got[node.Label] = true
	}
	for _, want := range []string{"CI pipeline 1", "Edge function 1", "Check 1", "Loop 1", "Entrance 1", "Timer 1", "Outside service 1"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
}

// A named repository publishes real labels, paths, rates and titles; a node
// with no source (runtime-only) stays an ordinal even beside it.
func TestPublicNamesRealForNamedRepositoriesOnly(t *testing.T) {
	graph := Graph{Nodes: []Node{
		{ID: "svc", Kind: Processor, Runtime: "server", Label: "api", Sources: []Location{{Repository: "r1", Path: "cmd/api/main.go"}}},
		{ID: "hidden", Kind: Processor, Runtime: "server", Label: "secret", Sources: []Location{{Repository: "r2", Path: "x.go"}}},
		{ID: "ghost", Kind: External, Label: "sender"},
	}}
	live := Overlay("s", graph, nil, nil, nil, 0, minute)
	named := map[string]bool{"r1": true, "o/n": true}
	world := Public(live, []byte("k"), nil, []Crate{{Key: "o/n#1", Repository: "o/n", Number: 1, Title: "Fix"}, {Key: "p/q#2", Repository: "p/q", Number: 2, Title: "Hide"}}, named, 0)
	labels := map[string]string{}
	for _, node := range world.Nodes {
		labels[node.Label] = strings.Join(node.Paths, ",")
	}
	if len(labels) != 3 || labels["api"] != "cmd/api/main.go" || labels["Web server 1"] != "" || labels["Outside service 1"] != "" {
		t.Fatalf("labels = %v", labels)
	}
	shown := 0
	for _, crate := range world.Crates {
		if crate.Title != "" {
			shown++
			if crate.Number != 1 || crate.Title != "Fix" {
				t.Fatalf("crate = %+v", crate)
			}
		}
	}
	if shown != 1 {
		t.Fatalf("crates = %+v", world.Crates)
	}
}
