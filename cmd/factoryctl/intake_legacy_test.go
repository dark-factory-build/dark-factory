package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestLegacyIntakeTransportRefusesWorkerMalformedAndOversizedInput(t *testing.T) {
	empty := func(string) string { return "" }
	for _, body := range []string{`{}`, `{"action":"legacy_commit"}`, strings.Repeat("x", (256<<10)+1)} {
		if code := runLegacyIntakeProtocol(context.Background(), "legacy_preview", strings.NewReader(body), empty, &bytes.Buffer{}); code != exitUsage {
			t.Fatalf("invalid transport exit %d", code)
		}
	}
	if code := runLegacyIntakeProtocol(context.Background(), "legacy_preview", strings.NewReader(`{}`), func(string) string { return "/worker-token" }, &bytes.Buffer{}); code != exitFailure {
		t.Fatalf("worker transport exit %d", code)
	}
}
