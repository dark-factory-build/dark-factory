package daemon

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLogFactorydPrefixesWithUTCRFC3339(t *testing.T) {
	var output bytes.Buffer
	when := time.Date(2026, 10, 5, 15, 6, 7, 0, time.FixedZone("BST", 60*60))
	logFactorydAt(&output, when, "factoryd: refresh timed out\n")

	got := strings.TrimSuffix(output.String(), "\n")
	parts := strings.SplitN(got, " ", 2)
	if len(parts) != 2 {
		t.Fatalf("timestamped line = %q", got)
	}
	parsed, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		t.Fatalf("timestamp = %q: %v", parts[0], err)
	}
	if !parsed.Equal(when.UTC()) || !strings.HasSuffix(parts[0], "Z") {
		t.Fatalf("timestamp = %q, want UTC RFC 3339 for %s", parts[0], when.UTC())
	}
	if got != "2026-10-05T14:06:07Z factoryd: refresh timed out" {
		t.Fatalf("line = %q", got)
	}
}
