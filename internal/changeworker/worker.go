package changeworker

import (
	"context"
	"errors"
)

var (
	ErrWorker      = errors.New("Change worker failed")
	ErrUnsupported = errors.New("Change worker unsupported")
)

// Run runs the one registered private Change-worker mode. Errors are
// deliberately fixed: repository, runtime, Change, token and input values are
// never exposed through this process boundary.
func Run(ctx context.Context) error {
	if ctx == nil {
		return ErrWorker
	}
	err := runProvider(ctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return errors.Join(ErrWorker, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrWorker, context.DeadlineExceeded)
	}
	if errors.Is(err, ErrUnsupported) {
		return ErrUnsupported
	}
	return ErrWorker
}
