//go:build !darwin

package install

import "context"

type operationalHomeState struct{}

func openOperationalHome(context.Context, string) (*OperationalHome, error) {
	return nil, ErrUnsupported
}

func (operationalHomeState) path(string) string { return "" }

func (operationalHomeState) close() error { return nil }
