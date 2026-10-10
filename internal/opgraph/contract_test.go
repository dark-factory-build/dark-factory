package opgraph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The console decodes the graph against its own GRAPH_* arrays; they must
// hold exactly the typed constants declared in graph.go, or a value one side
// adds is refused by the other in production.
func TestConsoleDecoderMatchesGraphValues(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "graph.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	goValues := map[string][]string{}
	ast.Inspect(file, func(node ast.Node) bool {
		if spec, ok := node.(*ast.ValueSpec); ok && spec.Type != nil {
			for _, item := range spec.Values {
				if literal, ok := item.(*ast.BasicLit); ok && literal.Kind == token.STRING {
					value, _ := strconv.Unquote(literal.Value)
					goValues[spec.Type.(*ast.Ident).Name] = append(goValues[spec.Type.(*ast.Ident).Name], value)
				}
			}
		}
		return true
	})
	source, err := os.ReadFile("../../web/packages/client/src/control.ts")
	if err != nil {
		t.Fatal(err)
	}
	tsValues := map[string][]string{}
	for _, match := range regexp.MustCompile(`const (GRAPH_\w+) = \[([^\]]*)\]`).FindAllStringSubmatch(string(source), -1) {
		for _, item := range strings.Split(match[2], ",") {
			value, _ := strconv.Unquote(strings.TrimSpace(item))
			tsValues[match[1]] = append(tsValues[match[1]], value)
		}
	}
	types := map[string]string{"GRAPH_NODE_KINDS": "Kind", "GRAPH_EDGE_KINDS": "EdgeKind", "GRAPH_EVIDENCE": "EvidenceState", "GRAPH_OBSERVATIONS": "Visibility",
		"GRAPH_STATES": "Activity", "GRAPH_RUNTIMES": "Placement", "GRAPH_TRIGGERS": "Trigger", "GRAPH_ORIGINS": "Origin"}
	if got, want := slices.Sorted(maps.Keys(tsValues)), slices.Sorted(maps.Keys(types)); !slices.Equal(got, want) {
		t.Fatalf("control.ts GRAPH_* arrays %v, want %v", got, want)
	}
	if got := tsValues["GRAPH_RUNTIMES"]; fmt.Sprint(got) != fmt.Sprint(runtimeOrder) {
		t.Errorf("control.ts GRAPH_RUNTIMES order %q differs from Locate's %q", got, runtimeOrder)
	}
	for name, kind := range types {
		if got, want := slices.Sorted(slices.Values(tsValues[name])), slices.Sorted(slices.Values(goValues[kind])); !slices.Equal(got, want) {
			t.Errorf("control.ts %s = %q, opgraph %s = %q", name, got, kind, want)
		}
	}
}
