//go:build !darwin

package runner

import (
	"errors"
	"testing"
)

func TestUnsupportedFailsBeforeEffect(t *testing.T) {
	if _, err := PrepareExecSpec(ExecSpec{Target: "/bin/sh"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v", err)
	}
	if err := RunExecGate(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("gate=%v", err)
	}
}
