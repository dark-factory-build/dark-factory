//go:build !darwin

package install

import (
	"context"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

type operationalHomeState struct{}

func openOperationalHome(context.Context, string) (*OperationalHome, error) {
	return nil, ErrUnsupported
}

func (operationalHomeState) memberCapability(string) (MemberCapability, error) {
	return MemberCapability{}, ErrUnsupported
}

func (operationalHomeState) openStore(context.Context) (*kernel.Store, error) {
	return nil, ErrUnsupported
}

func (operationalHomeState) openCapability(string) (*os.File, error) {
	return nil, ErrUnsupported
}

func (operationalHomeState) close() error { return nil }
