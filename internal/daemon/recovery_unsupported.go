//go:build !darwin

package daemon

import (
	"context"
	"errors"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// ContinueUnsettledRun is unavailable where the Darwin runtime authority is
// not compiled in.
func (daemon *Daemon) ContinueUnsettledRun(context.Context, *RuntimeParent, string, kernel.RunID) error {
	return errors.New("daemon: unsettled runtime continuation unsupported")
}
