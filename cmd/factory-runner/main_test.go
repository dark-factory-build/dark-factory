package main

import (
	"errors"
	"testing"
)

func TestDirectInvocationRequiresExactPrivateMode(t *testing.T) {
	for _, args := range [][]string{nil, {"--version"}, {"--exec-gate", "extra"}, {"--attempt-runner", "extra"}} {
		if err := run(args); !errors.Is(err, errPrivateCapability) {
			t.Fatalf("args=%q err=%v", args, err)
		}
	}
}
