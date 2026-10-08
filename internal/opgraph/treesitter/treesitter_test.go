package treesitter

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseAndQuery(t *testing.T) {
	p, err := New(context.Background(), "javascript")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	q, err := p.Query(`(call_expression function: (member_expression property: (property_identifier) @method) arguments: (arguments . (string (string_fragment) @route)))`)
	if err != nil {
		t.Fatal(err)
	}
	// Multi-byte and astral runes before the match: offsets are UTF-8 bytes.
	source := "const s = \"é😀\";\napp.get('/items', list)\n// app.get('/comment')\nrouter.post(\"/x\", h)\n"
	tree, err := p.Parse([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	matches, err := tree.Matches(q)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v", matches)
	}
	for index, want := range []Capture{{"route", "/items", strings.Index(source, "/items"), strings.Index(source, "/items") + 6, 2}, {"route", "/x", strings.Index(source, "/x"), strings.Index(source, "/x") + 2, 4}} {
		if got := matches[index].Captures[1]; got != want {
			t.Errorf("capture %d = %+v, want %+v", index, got, want)
		}
	}
	if _, err := p.Query(`(no_such_node) @x`); err == nil {
		t.Fatal("an invalid query compiled")
	}
}

func TestEveryGrammarLinks(t *testing.T) {
	for language, source := range map[string]string{
		"javascript": "f()", "typescript": "let x: number = 1", "tsx": "const a = <div/>", "python": "def f(): pass",
		"ruby": "def f; end", "java": "class A {}", "kotlin": "fun f() {}", "rust": "fn main() {}",
	} {
		p, err := New(context.Background(), language)
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		tree, err := p.Parse([]byte(source))
		if err != nil {
			t.Fatalf("%s: %v", language, err)
		}
		tree.Close()
		p.Close()
	}
	if _, err := New(context.Background(), "cobol"); err == nil {
		t.Fatal("an unknown language loaded")
	}
}

// A file past the work or memory bound fails alone: the parser reports it
// and the same parser (or, after a trap, a new one) keeps working.
func TestBoundsDegradeOneFile(t *testing.T) {
	p, err := New(context.Background(), "ruby")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.Close() }()
	_, err = p.Parse([]byte(strings.Repeat("x = foo(1, \"a\") + bar[2]\n", 40000)))
	if err == nil {
		t.Fatal("a megabyte parsed within one file's budget")
	}
	if !errors.Is(err, ErrBudget) {
		p.Close()
		if p, err = New(context.Background(), "ruby"); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := p.Parse([]byte("get '/x'"))
	if err != nil {
		t.Fatalf("parser unusable after a failed file: %v", err)
	}
	tree.Close()
}

// A file stopped by its budget leaves nothing behind: the next file of the
// same grammar parses from its own start and matches its own text.
func TestBudgetStopDoesNotLeakIntoTheNextFile(t *testing.T) {
	p, err := New(context.Background(), "javascript")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { p.Close() }()
	if _, err := p.Parse([]byte(strings.Repeat("app.get(\"/old\", h); x = foo(1, \"a\") + bar[2];\n", 40000))); !errors.Is(err, ErrBudget) {
		t.Fatalf("expected the huge file to exhaust its budget, got %v", err)
	}
	q, err := p.Query(`(call_expression function: (member_expression property: (property_identifier) @method) arguments: (arguments . (string (string_fragment) @route)))`)
	if err != nil {
		t.Fatal(err)
	}
	source := `app.get("/new", handler);`
	tree, err := p.Parse([]byte(source))
	if err != nil {
		t.Fatalf("parser unusable after a stopped file: %v", err)
	}
	defer tree.Close()
	matches, err := tree.Matches(q)
	if err != nil {
		t.Fatal(err)
	}
	want := Capture{"route", "/new", strings.Index(source, "/new"), strings.Index(source, "/new") + 4, 1}
	if len(matches) != 1 || matches[0].Captures[1] != want {
		t.Fatalf("matches after a stopped file = %+v, want only %+v", matches, want)
	}
}

// Every pinned build must ship with its licence in THIRD_PARTY_NOTICES.
func TestPinnedBuildsHaveLicences(t *testing.T) {
	licences, err := os.ReadFile("wasm/LICENSES.txt")
	if err != nil {
		t.Fatal(err)
	}
	for name, pin := range pins {
		if !strings.Contains(string(licences), "== "+pin.source) {
			t.Errorf("%s: no licence for %s in wasm/LICENSES.txt", name, pin.source)
		}
	}
}
