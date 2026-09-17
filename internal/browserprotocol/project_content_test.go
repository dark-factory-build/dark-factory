package browserprotocol

import (
	"errors"
	"strings"
	"testing"
)

func TestProjectContentContractIsFiniteAndBounded(t *testing.T) {
	wire, err := EncodeProjectContent("content", ProjectContent{Operation: "body", Input: []byte(`{"project_id":"02020202020202020202020202020202","content_id":"01010101010101010101010101010101","revision":1,"offset":0,"limit":8192}`)})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := DecodeClientControl(wire)
	if err != nil || frame.Type != TypeProjectContent {
		t.Fatalf("request = %+v, %v", frame, err)
	}
	result, err := EncodeProjectContentResult("content", ProjectContentResult{Operation: "body", Output: []byte(`{"body":"ok","complete":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerControl(result); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerControl(wire); err == nil {
		t.Fatal("client message crossed server direction")
	}
	if _, err := EncodeProjectContent("content", ProjectContent{Operation: "unknown", Input: []byte(`{}`)}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("unknown operation = %v", err)
	}
	if _, err := EncodeProjectContent("content", ProjectContent{Operation: "list", Input: []byte(`[]`)}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("array input = %v", err)
	}
	if _, err := EncodeProjectContent("content", ProjectContent{Operation: "list", Input: []byte(`{"project_id":"x","padding":"` + strings.Repeat("x", MaxControlBytes) + `"}`)}); !errors.Is(err, ErrOversized) {
		t.Fatalf("oversized input = %v", err)
	}
}
