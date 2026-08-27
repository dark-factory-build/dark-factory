//go:build !darwin

package daemon

import (
	"context"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func startLiveAttempt(*liveAttempt, context.Context) {}

func (attempt *liveAttempt) releaseProvider(context.Context) error { return runner.ErrUnsupported }

func (attempt *liveAttempt) attach(context.Context, uint64) (*TerminalAttachment, error) {
	return nil, runner.ErrUnsupported
}

func (attempt *liveAttempt) detach(context.Context, *TerminalAttachment) error {
	return runner.ErrUnsupported
}

func (attempt *liveAttempt) acknowledge(context.Context, *runner.TerminalRecord) error {
	return runner.ErrUnsupported
}
