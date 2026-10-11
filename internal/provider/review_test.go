package provider

import "testing"

func TestReviewUsageLimitedRequiresProviderMarkers(t *testing.T) {
	for _, test := range []struct {
		name, output string
		want         bool
	}{
		{"quoted limit", "the task says: You've hit your usage limit", false},
		{"usage limit", "■ You've hit your usage limit", true},
		{"styled usage limit", "\x1b[31m■\x1b[0m You've hit your usage limit", true},
		{"model capacity", "⚠ Selected model is at capacity", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ReviewUsageLimited(test.output); got != test.want {
				t.Fatalf("ReviewUsageLimited(%q) = %v, want %v", test.output, got, test.want)
			}
		})
	}
}
