//go:build !darwin

package change

import (
	"context"
	"runtime"
)

func InspectRepositorySource(context.Context, string, string, string, RepositoryIdentity) (RepositorySourceIdentity, error) {
	return RepositorySourceIdentity{}, &UnsupportedError{Platform: runtime.GOOS}
}

func SelectRegisteredGit(context.Context, string, string, string, RepositorySourceIdentity) (Selection, error) {
	return Selection{}, &UnsupportedError{Platform: runtime.GOOS}
}
