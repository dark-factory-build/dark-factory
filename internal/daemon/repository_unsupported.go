//go:build !darwin

package daemon

import (
	"context"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func inspectRegisteredRepository(context.Context, string, string) (kernel.RepositorySourceIdentity, error) {
	return kernel.RepositorySourceIdentity{}, kernel.ErrInvalidValue
}
