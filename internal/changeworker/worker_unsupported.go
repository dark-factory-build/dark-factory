//go:build !darwin

package changeworker

import "context"

func runShell(context.Context) error { return ErrUnsupported }
