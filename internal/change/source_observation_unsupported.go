//go:build !darwin

package change

import (
	"context"
	"runtime"
)

func ArchiveSource(context.Context, string, string, string, RepositorySourceIdentity) (string, []byte, error) {
	return "", nil, &UnsupportedError{Platform: runtime.GOOS}
}
func ObserveSource(context.Context, string, string, string, string, string, RepositorySourceIdentity) (SourceObservation, error) {
	return SourceObservation{}, &UnsupportedError{Platform: runtime.GOOS}
}
