//go:build darwin

package runner

import (
	"os"
	"testing"
)

func TestShortTemporaryDirectories(t *testing.T) {
	if got := PrivateTemp("/factory/runtimes/cd518a4e3b32892d683def53f31d8340"); got != "/private/tmp/df-cd518a4e" {
		t.Fatalf("PrivateTemp = %q", got)
	}
	for _, runtime := range []string{"/tests/001", "/factory/runtimes/CD518A4E3B32892D683DEF53F31D8340"} {
		if got := PrivateTemp(runtime); got != "" {
			t.Fatalf("PrivateTemp(%q) = %q, want none", runtime, got)
		}
	}
	short, err := os.MkdirTemp(ShortTempDir(), "short-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	// A TMPDIR beneath the shared directory, as a sandboxed run's is, is used
	// resolved; any other (macOS's deep per-user one) keeps the shared one.
	for temp, want := range map[string]string{"/tmp": "/private/tmp", "/private/var/folders": "/private/tmp", short: short, "/tmp" + short[len("/private/tmp"):]: short} {
		t.Setenv("TMPDIR", temp)
		if got := ShortTempDir(); got != want {
			t.Fatalf("TMPDIR=%q: ShortTempDir = %q, want %q", temp, got, want)
		}
	}
}
