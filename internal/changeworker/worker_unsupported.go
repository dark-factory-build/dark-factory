//go:build !darwin

package changeworker

import "context"

func runProvider(context.Context) error { return ErrUnsupported }

func MaterializeRetainedSource(context.Context, string, string, RetainedSource) (string, error) {
	return "", ErrUnsupported
}
