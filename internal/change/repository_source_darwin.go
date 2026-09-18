//go:build darwin

package change

import (
	"context"
	"strings"
)

// InspectRepositorySource proves the configured root and base without fetching
// or changing refs. An empty base only inspects identity; normal source selection
// still refreshes and validates tracking revisions.
func InspectRepositorySource(ctx context.Context, gitExecutable, root, base string, expected RepositoryIdentity) (RepositorySourceIdentity, error) {
	if base != "" {
		if err := validateRevision(base); err != nil {
			return RepositorySourceIdentity{}, err
		}
	}
	authority, err := openGitAuthority(gitExecutable, root, expected, nil, true)
	if err != nil {
		return RepositorySourceIdentity{}, err
	}
	defer authority.close()
	if base == "" {
		output, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", root, "rev-parse", "--show-toplevel")
		if err != nil {
			return RepositorySourceIdentity{}, err
		}
		if string(output) != root+"\n" {
			return RepositorySourceIdentity{}, &ValidationError{Reason: "registered root is not the Git checkout root"}
		}
		return authority.sourceIdentity(ctx)
	}
	output, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", root, "rev-parse", "--show-toplevel", "--show-object-format", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return RepositorySourceIdentity{}, err
	}
	if _, _, err := parseSelectionOutput(root, output); err != nil {
		return RepositorySourceIdentity{}, err
	}
	return authority.sourceIdentity(ctx)
}

func (authority *gitAuthority) sourceIdentity(ctx context.Context) (RepositorySourceIdentity, error) {
	values := make([]string, 2)
	for i, key := range []string{"remote.origin.url", "remote.origin.pushurl"} {
		result, err := authority.run(ctx, maxGitSelectionOutput, "-C", authority.repositoryRoot, "config", "--local", "--null", "--get-all", key)
		if err != nil {
			return RepositorySourceIdentity{}, err
		}
		if result.exitCode == 1 && len(result.output) == 0 {
			continue
		}
		value := string(result.output)
		if result.exitCode != 0 || !strings.HasSuffix(value, "\x00") || strings.Count(value, "\x00") != 1 {
			return RepositorySourceIdentity{}, &ValidationError{Reason: "origin must have one fetch and publication target"}
		}
		values[i] = strings.TrimSuffix(value, "\x00")
	}
	// Git expands insteadOf/pushInsteadOf before transport. Bind the effective
	// destinations, so a later local rewrite cannot silently retarget work.
	if values[0] != "" {
		for i := range values {
			arguments := []string{"-C", authority.repositoryRoot, "remote", "get-url", "--all"}
			if i == 1 {
				arguments = append(arguments, "--push")
			}
			output, err := authority.succeed(ctx, maxGitSelectionOutput, append(arguments, "origin")...)
			if err != nil {
				return RepositorySourceIdentity{}, err
			}
			value := string(output)
			if !strings.HasSuffix(value, "\n") || strings.Count(value, "\n") != 1 || strings.ContainsAny(value, "\x00\r") {
				return RepositorySourceIdentity{}, &ValidationError{Reason: "origin must have one effective fetch and publication target"}
			}
			values[i] = strings.TrimSuffix(value, "\n")
		}
	}
	digest, publication, err := remoteSourceDigest(values[0], values[1])
	if err != nil {
		return RepositorySourceIdentity{}, err
	}
	git, err := NewRepositoryIdentity(authority.repository.git.device, authority.repository.git.inode)
	if err != nil {
		return RepositorySourceIdentity{}, err
	}
	return RepositorySourceIdentity{Root: authority.repository.root, Git: git, OriginDigest: digest, PublicationRepository: publication}, nil
}
