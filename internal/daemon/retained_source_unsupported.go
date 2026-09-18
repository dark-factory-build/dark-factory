//go:build !darwin

package daemon

import (
	"context"
	"errors"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) attemptSourceHandoff(context.Context, kernel.RetainedChangeHandoff) (api.RetainedChangeHandoff, error) {
	return api.RetainedChangeHandoff{}, errors.Join(install.ErrUnsupported, kernel.ErrConflict)
}
