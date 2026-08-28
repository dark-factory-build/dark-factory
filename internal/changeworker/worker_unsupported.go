//go:build !darwin

package changeworker

import "context"

func runProvider(context.Context) error { return ErrUnsupported }
