//go:build !darwin

package daemon

import (
	"context"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) runNext(context.Context, SupervisorSpec) (kernel.Run, error) {
	return kernel.Run{}, errUnsupported
}
