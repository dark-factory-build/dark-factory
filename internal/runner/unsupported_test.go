//go:build !darwin

package runner

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestUnsupportedFailsBeforeEffect(t *testing.T) {
	if _, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	if err := RunExecGate(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("gate=%v", err)
	}
	if err := RunAttemptRunner(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("attempt=%v", err)
	}
	if _, _, err := NewAttemptController(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("controller=%v", err)
	}
	cwd, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fd := int(cwd.Fd())
	var worker *WorkerControl
	if err := worker.ExecProvider(nil, cwd); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("provider=%v", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); !errors.Is(err, unix.EBADF) {
		t.Fatalf("unsupported cwd remained open: %v", err)
	}
}
