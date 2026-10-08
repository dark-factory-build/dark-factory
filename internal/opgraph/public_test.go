package opgraph

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every private field carries a canary; none may reach the public projection.
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
	world := Public(live, []byte("secret"), []Worker{{Activity: "busy", Unit: unit}}, []Crate{{Key: canary + "/repo#7", Station: 2, Fault: true}}, 123_456_789)
	encoded, _ := json.Marshal(world)
	if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(canary)) || strings.Contains(string(encoded), unit) {
		t.Fatalf("private data leaked: %s", encoded)
	}
	if world.GeneratedAt%(5*60_000) != 0 || len(world.Workers) != 1 || world.Workers[0].Unit == "" || len(world.Crates) != 1 || world.Crates[0] != (PublicCrate{ID: world.Crates[0].ID, Station: 2, Fault: true}) || len(world.Crates[0].ID) != 32 {
		t.Fatalf("world = %+v", world)
	}
	again := Public(live, []byte("secret"), nil, nil, 0)
	other := Public(live, []byte("another"), nil, nil, 0)
	if again.Nodes[0].ID != world.Nodes[0].ID || other.Nodes[0].ID == world.Nodes[0].ID {
		t.Fatal("public identities are not stable per secret")
	}
	for _, node := range world.Nodes {
		if node.Activity != "none" && node.Observation == "unobserved" {
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
