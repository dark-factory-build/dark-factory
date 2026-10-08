package treesitter

import (
	"context"
	"errors"
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
