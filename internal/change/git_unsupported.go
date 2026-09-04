//go:build !darwin

package change

import (
	"context"
	"runtime"
)

// GitBlobs exists only to keep unsupported builds source-compatible.
type GitBlobs struct{}

func SelectGit(context.Context, string, string, string, RepositoryIdentity) (Selection, error) {
	return Selection{}, &UnsupportedError{Platform: runtime.GOOS}
}

func VerifyRepositoryRoot(string, RepositoryIdentity) error {
	return &UnsupportedError{Platform: runtime.GOOS}
}

func OpenGitBlobs(context.Context, string, string, Selection) (*GitBlobs, error) {
	return nil, &UnsupportedError{Platform: runtime.GOOS}
}

func (b *GitBlobs) Read(context.Context, ObjectID) ([]byte, error) {
	return nil, &UnsupportedError{Platform: runtime.GOOS}
}

func (b *GitBlobs) Close() error { return &UnsupportedError{Platform: runtime.GOOS} }

func (b *GitBlobs) Abort() error { return &UnsupportedError{Platform: runtime.GOOS} }
