//go:build !darwin

package daemon

import (
	"context"
	"errors"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) materializeAttemptSource(context.Context, *liveAttempt, kernel.RetainedChangeHandoff) (string, error) {
	return "", errors.Join(install.ErrUnsupported, kernel.ErrConflict)
}
