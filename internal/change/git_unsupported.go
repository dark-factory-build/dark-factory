//go:build !darwin

package change

import (
	"context"
	"runtime"

	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
)

func SelectGit(context.Context, string, string, string, RepositoryIdentity) (Selection, error) {
	return Selection{}, &UnsupportedError{Platform: runtime.GOOS}
}

func VerifyRepositoryRoot(string, RepositoryIdentity) error {
	return &UnsupportedError{Platform: runtime.GOOS}
}

func AddWorktree(context.Context, Selection, string, string) (WorktreeFacts, error) {
	return WorktreeFacts{}, &UnsupportedError{Platform: runtime.GOOS}
}

func InspectWorktree(context.Context, string, string, RepositoryIdentity, string) (WorktreeFacts, error) {
	return WorktreeFacts{}, &UnsupportedError{Platform: runtime.GOOS}
}

func DescendsFrom(context.Context, string, string, RepositoryIdentity, string, ObjectID) (bool, error) {
	return false, &UnsupportedError{Platform: runtime.GOOS}
}

func AdoptWorktree(context.Context, string, string, RepositoryIdentity, string, string, ObjectID) (WorktreeFacts, error) {
	return WorktreeFacts{}, &UnsupportedError{Platform: runtime.GOOS}
}

func PinContentSource(context.Context, string, string, RepositoryIdentity, string, string, string) (ContentSource, error) {
	return ContentSource{}, &UnsupportedError{Platform: runtime.GOOS}
}

func ReadContentSource(context.Context, string, string, RepositoryIdentity, ContentSource) (string, error) {
	return "", &UnsupportedError{Platform: runtime.GOOS}
}

func WriteContentSource(context.Context, string, string, RepositoryIdentity, *ContentSource, string, string, string, gitauthor.Identity) (ContentSource, error) {
	return ContentSource{}, &UnsupportedError{Platform: runtime.GOOS}
}
