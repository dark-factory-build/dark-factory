//go:build darwin

package change

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ReviewCheckout makes path a disposable clone checked out at exactly head,
// the head of ref (a pull request's or a branch's), with base present. The
// clone borrows the registered repository's objects through Git alternates
// and fetches ref from that repository's registered origin into its own refs,
// so the repository's refs, config and index are only ever read. An empty ref
// fetches the base alone and checks out head, which must be merged into it.
func ReviewCheckout(ctx context.Context, gitExecutable, root string, expected RepositorySourceIdentity, path string, ref, head, base, baseRef string) error {
	if err := validateRevision(baseRef); err != nil {
		return err
	}
	for _, value := range []string{head, base} {
		if raw, err := hex.DecodeString(value); err != nil || (len(raw) != 20 && len(raw) != 32) || value != strings.ToLower(value) {
			return &ValidationError{Reason: "review commit must be a full lowercase object ID"}
		}
	}
	authority, err := openGitAuthority(gitExecutable, root, expected.Root, nil, true)
	if err != nil {
		return err
	}
	defer authority.close()
	actual, err := authority.sourceIdentity(ctx)
	if err != nil {
		return err
	}
	if actual.Root != expected.Root || actual.Git != expected.Git || actual.OriginDigest != expected.OriginDigest {
		return &ValidationError{Reason: "registered checkout identity changed"}
	}
	origin, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", root, "remote", "get-url", "--", "origin")
	if err != nil {
		return err
	}
	if _, err := authority.succeed(ctx, maxGitSelectionOutput, "init", "--quiet", "--", path); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(path, ".git", "objects", "info", "alternates"), []byte(filepath.Join(root, ".git", "objects")+"\n"), 0o600); err != nil {
		return newGitError(gitFailurePrivateIO)
	}
	// The origin is the registered, digest-bound remote, so a local-path
	// origin (a mirror, or a test fixture) is as trusted as a GitHub one.
	fetch := []string{"-C", path, "-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always", "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--end-of-options", strings.TrimSpace(string(origin)), "+refs/heads/" + strings.TrimPrefix(baseRef, "refs/heads/") + ":refs/review/base"}
	checkout := head
	if ref != "" {
		fetch, checkout = append(fetch, "+"+ref+":refs/review/head"), "refs/review/head"
	}
	if _, err := authority.succeed(ctx, maxGitSelectionOutput, fetch...); err != nil {
		return err
	}
	if ref == "" {
		if merged, err := authority.run(ctx, maxGitSelectionOutput, "-C", path, "merge-base", "--is-ancestor", head, "refs/review/base"); err != nil || merged.exitCode != 0 {
			return errors.Join(&ValidationError{Reason: "commit is not merged into the base"}, err)
		}
	}
	if _, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", path, "-c", "core.hooksPath=/dev/null", "checkout", "--quiet", "--detach", checkout); err != nil {
		return err
	}
	checked, err := authority.run(ctx, maxGitSelectionOutput, "-C", path, "rev-parse", "HEAD^{commit}", base+"^{commit}")
	if err != nil {
		return err
	}
	if checked.exitCode != 0 || string(checked.output) != head+"\n"+base+"\n" {
		return &ValidationError{Reason: "pull request head or base differs from the requested review"}
	}
	return nil
}

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
