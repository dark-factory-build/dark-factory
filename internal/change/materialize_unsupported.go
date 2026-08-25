//go:build !darwin

package change

import (
	"context"
	"runtime"
)

func materialize(context.Context, string, string, Manifest, BlobSource, materializeHook) (MaterializeResult, error) {
	return MaterializeResult{}, &UnsupportedError{Platform: runtime.GOOS}
}
