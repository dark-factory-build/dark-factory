//go:build darwin

package change

import (
	"context"
	"crypto/sha256"
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
	// Every configured remote can be a branch's upstream. Pin all effective
	// destinations, including Git URL rewrites, without retaining credentials.
	output, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", authority.repositoryRoot, "remote")
	if err != nil {
		return RepositorySourceIdentity{}, err
	}
	remoteNames := strings.Fields(string(output))
	if len(remoteNames) > 0 && strings.Join(remoteNames, "\n")+"\n" != string(output) {
		return RepositorySourceIdentity{}, &ValidationError{Reason: "invalid registered remote names"}
	}
	digest := sha256.New()
	publication := ""
	for _, remote := range remoteNames {
		values := make([]string, 2)
		for i := range values {
			arguments := []string{"-C", authority.repositoryRoot, "remote", "get-url", "--all"}
			if i == 1 {
				arguments = append(arguments, "--push")
			}
			output, err := authority.succeed(ctx, maxGitSelectionOutput, append(arguments, "--", remote)...)
			if err != nil {
				return RepositorySourceIdentity{}, err
			}
			value := string(output)
			if !strings.HasSuffix(value, "\n") || strings.Count(value, "\n") != 1 || strings.ContainsAny(value, "\x00\r") {
				return RepositorySourceIdentity{}, &ValidationError{Reason: "each remote must have one effective fetch and publication target"}
			}
			values[i] = strings.TrimSuffix(value, "\n")
		}
		digest.Write([]byte(remote + "\x00" + values[0] + "\x00" + values[1] + "\x00"))
		if remote == "origin" {
			_, publication, err = remoteSourceDigest(values[0], values[1])
			if err != nil {
				return RepositorySourceIdentity{}, err
			}
		}
	}
	var originDigest [32]byte
	copy(originDigest[:], digest.Sum(nil))
	git, err := NewRepositoryIdentity(authority.repository.git.device, authority.repository.git.inode)
	if err != nil {
		return RepositorySourceIdentity{}, err
	}
	return RepositorySourceIdentity{Root: authority.repository.root, Git: git, OriginDigest: originDigest, PublicationRepository: publication}, nil
}
