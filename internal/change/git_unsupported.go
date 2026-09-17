//go:build !darwin

package change

import (
	"context"
	"runtime"
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
