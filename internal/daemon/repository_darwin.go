//go:build darwin

package daemon

import (
	"context"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func inspectRegisteredRepository(ctx context.Context, root, base string) (kernel.RepositorySourceIdentity, error) {
	identity, err := inspectRepositoryIdentity(root)
	if err != nil {
		return kernel.RepositorySourceIdentity{}, err
	}
	value, err := change.InspectRepositorySource(ctx, change.TrustedGitExecutable, root, base, identity)
	if err != nil {
		return kernel.RepositorySourceIdentity{}, fmt.Errorf("%w: registered source unavailable: %v", kernel.ErrInvalidValue, err)
	}
	return kernel.RepositorySourceIdentity{RootDevice: value.Root.Device(), RootInode: value.Root.Inode(), GitDevice: value.Git.Device(), GitInode: value.Git.Inode(), OriginDigest: value.OriginDigest, PublicationRepository: value.PublicationRepository}, nil
}
