//go:build !darwin

package daemon

import (
	"context"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) validateSuccessSource(context.Context, *liveAttempt, kernel.Proposal) error {
	return nil
}
