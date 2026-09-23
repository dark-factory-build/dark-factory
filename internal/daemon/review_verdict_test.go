package daemon

import "testing"

func TestTerminalReviewVerdictRejectsMixedProviderOutput(t *testing.T) {
	if _, err := terminalReviewVerdict("analysis\nVERDICT: ALLOW\nmore output\nVERDICT: REQUEST_CHANGES\n"); err == nil {
		t.Fatal("mixed provider output was accepted")
	}
	if _, err := terminalReviewVerdict("analysis\nVERDICT: ALLOW\n"); err != nil {
		t.Fatalf("single terminal verdict rejected: %v", err)
	}
}
