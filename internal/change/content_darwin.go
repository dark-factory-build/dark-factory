//go:build darwin

package change

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxContentSourceBytes = 1 << 20

func contentWriteGitArguments(repositoryRoot string, arguments ...string) []string {
	result := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsync=all", "-c", "core.fsyncMethod=fsync", "-C", repositoryRoot}
	return append(result, arguments...)
}

// PinContentSource verifies one regular UTF-8 repository file at commit and
// creates a private durable ref to the commit. It never accepts a branch name
// as a durable source and never writes the worktree or creates a commit.
func PinContentSource(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, commit, file, durableRef string) (ContentSource, error) {
	if err := validateRevision(commit); err != nil {
		return ContentSource{}, err
	}
	if err := validateContentPath(file); err != nil {
		return ContentSource{}, err
	}
	if !validContentRef(durableRef) {
		return ContentSource{}, &ValidationError{Reason: "content durable ref is invalid"}
	}
	authority, err := openGitAuthority(gitExecutable, repositoryRoot, expected, nil, true)
	if err != nil {
		return ContentSource{}, err
	}
	defer authority.close()
	formatOutput, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", repositoryRoot, "rev-parse", "--show-object-format")
	if err != nil {
		return ContentSource{}, err
	}
	format, err := NewObjectFormat(strings.TrimSpace(string(formatOutput)))
	if err != nil {
		return ContentSource{}, newGitError(gitFailureProtocol)
	}
	resolved, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", repositoryRoot, "rev-parse", "--verify", "--end-of-options", commit+"^{commit}")
	if err != nil {
		return ContentSource{}, err
	}
	id, err := parseGitOID(format, bytes.TrimSpace(resolved))
	if err != nil {
		return ContentSource{}, err
	}
	object := id.Hex() + ":" + file
	tree, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", repositoryRoot, "ls-tree", "-z", id.Hex(), "--", file)
	if err != nil || !regularContentTreeEntry(tree, file) {
		if err == nil {
			err = &ValidationError{Reason: "content source must be a regular file"}
		}
		return ContentSource{}, err
	}
	body, err := authority.succeed(ctx, maxContentSourceBytes, "-C", repositoryRoot, "show", "--no-textconv", "--format=", object)
	if err != nil || !utf8.Valid(body) {
		if err != nil {
			return ContentSource{}, err
		}
		return ContentSource{}, &ValidationError{Reason: "content source must be valid UTF-8"}
	}
	if err := updateContentRef(ctx, authority, durableRef, id, format); err != nil {
		return ContentSource{}, err
	}
	return ContentSource{Commit: id, Path: file}, nil
}

// ReadContentSource returns the exact pinned file without consulting HEAD.
func ReadContentSource(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, source ContentSource) (string, error) {
	if !source.Commit.Format().valid() || validateContentPath(source.Path) != nil {
		return "", &ValidationError{Reason: "content source is invalid"}
	}
	authority, err := openGitAuthority(gitExecutable, repositoryRoot, expected, nil, true)
	if err != nil {
		return "", err
	}
	defer authority.close()
	return readContentSource(ctx, authority, source)
}

// WriteContentSource creates a private content commit from body without
// changing HEAD, the checked-out files, or the ordinary repository index.
// Git's commit author identifies this generated object only; the daemon keeps
// the authenticated actor in SQLite.
func WriteContentSource(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, parent *ContentSource, file, body, durableRef string, author gitauthor.Identity) (ContentSource, error) {
	if err := validateContentPath(file); err != nil || !author.Valid() || len(body) > maxContentSourceBytes || !utf8.ValidString(body) || !validContentRef(durableRef) {
		return ContentSource{}, &ValidationError{Reason: "content write is invalid"}
	}
	authority, err := openGitAuthority(gitExecutable, repositoryRoot, expected, nil, true)
	if err != nil {
		return ContentSource{}, err
	}
	defer authority.close()
	if existing, found, err := existingContentSource(ctx, authority, durableRef, file); err != nil {
		return ContentSource{}, err
	} else if found {
		stored, err := readContentSource(ctx, authority, existing)
		if err != nil || stored != body {
			if err == nil {
				err = &ValidationError{Reason: "content durable ref already has different content"}
			}
			return ContentSource{}, err
		}
		if err := updateContentRef(ctx, authority, durableRef, existing.Commit, existing.Commit.Format()); err != nil {
			return ContentSource{}, err
		}
		return existing, nil
	}
	formatOutput, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", repositoryRoot, "rev-parse", "--show-object-format")
	if err != nil {
		return ContentSource{}, err
	}
	format, err := NewObjectFormat(strings.TrimSpace(string(formatOutput)))
	if err != nil {
		return ContentSource{}, newGitError(gitFailureProtocol)
	}
	base := "HEAD^{commit}"
	if parent != nil {
		if !parent.Commit.Format().valid() || validateContentPath(parent.Path) != nil {
			return ContentSource{}, &ValidationError{Reason: "content parent is invalid"}
		}
		base = parent.Commit.Hex()
	}
	baseOutput, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", repositoryRoot, "rev-parse", "--verify", "--end-of-options", base)
	if err != nil {
		return ContentSource{}, err
	}
	baseID, err := parseGitOID(format, bytes.TrimSpace(baseOutput))
	if err != nil {
		return ContentSource{}, err
	}
	bodyFile := filepath.Join(authority.home, "content")
	if err := os.WriteFile(bodyFile, []byte(body), 0o600); err != nil {
		return ContentSource{}, newGitError(gitFailurePrivateIO)
	}
	blobOutput, err := authority.succeed(ctx, maxGitSelectionOutput, contentWriteGitArguments(repositoryRoot, "hash-object", "-w", "--no-filters", "--", bodyFile)...)
	if err != nil {
		return ContentSource{}, err
	}
	blob, err := parseGitOID(format, bytes.TrimSpace(blobOutput))
	if err != nil {
		return ContentSource{}, err
	}
	index := filepath.Join(authority.home, "index")
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := authority.succeedWithEnvironment(ctx, maxGitSelectionOutput, env, "-C", repositoryRoot, "read-tree", baseID.Hex()); err != nil {
		return ContentSource{}, err
	}
	if _, err := authority.succeedWithEnvironment(ctx, maxGitSelectionOutput, env, "-C", repositoryRoot, "update-index", "--add", "--cacheinfo", "100644,"+blob.Hex()+","+file); err != nil {
		return ContentSource{}, err
	}
	treeOutput, err := authority.succeedWithEnvironment(ctx, maxGitSelectionOutput, env, contentWriteGitArguments(repositoryRoot, "write-tree")...)
	if err != nil {
		return ContentSource{}, err
	}
	tree, err := parseGitOID(format, bytes.TrimSpace(treeOutput))
	if err != nil {
		return ContentSource{}, err
	}
	commitEnv := append(env, "GIT_AUTHOR_NAME="+author.Name(), "GIT_AUTHOR_EMAIL="+author.Email(), "GIT_COMMITTER_NAME="+author.Name(), "GIT_COMMITTER_EMAIL="+author.Email())
	commitOutput, err := authority.succeedWithEnvironment(ctx, maxGitSelectionOutput, commitEnv, contentWriteGitArguments(repositoryRoot, "commit-tree", tree.Hex(), "-p", baseID.Hex(), "-m", "Update project content")...)
	if err != nil {
		return ContentSource{}, err
	}
	commitID, err := parseGitOID(format, bytes.TrimSpace(commitOutput))
	if err != nil {
		return ContentSource{}, err
	}
	if err := updateContentRef(ctx, authority, durableRef, commitID, format); err != nil {
		existing, found, readErr := existingContentSource(ctx, authority, durableRef, file)
		if readErr != nil || !found {
			return ContentSource{}, err
		}
		stored, readErr := readContentSource(ctx, authority, existing)
		if readErr != nil || stored != body {
			return ContentSource{}, err
		}
		return existing, nil
	}
	return ContentSource{Commit: commitID, Path: file}, nil
}

func existingContentSource(ctx context.Context, authority *gitAuthority, durableRef, file string) (ContentSource, bool, error) {
	formatOutput, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", authority.repositoryRoot, "rev-parse", "--show-object-format")
	if err != nil {
		return ContentSource{}, false, err
	}
	format, err := NewObjectFormat(strings.TrimSpace(string(formatOutput)))
	if err != nil {
		return ContentSource{}, false, newGitError(gitFailureProtocol)
	}
	current, err := authority.run(ctx, maxGitSelectionOutput, "-C", authority.repositoryRoot, "rev-parse", "--verify", "--quiet", "--end-of-options", durableRef+"^{commit}")
	if err != nil {
		return ContentSource{}, false, err
	}
	if current.exitCode == 1 {
		return ContentSource{}, false, nil
	}
	if current.exitCode != 0 {
		return ContentSource{}, false, newGitError(gitFailureProcess)
	}
	id, err := parseGitOID(format, bytes.TrimSpace(current.output))
	if err != nil {
		return ContentSource{}, false, err
	}
	tree, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", authority.repositoryRoot, "ls-tree", "-z", id.Hex(), "--", file)
	if err != nil || !regularContentTreeEntry(tree, file) {
		if err == nil {
			err = &ValidationError{Reason: "content source must be a regular file"}
		}
		return ContentSource{}, false, err
	}
	return ContentSource{Commit: id, Path: file}, true, nil
}

func readContentSource(ctx context.Context, authority *gitAuthority, source ContentSource) (string, error) {
	body, err := authority.succeed(ctx, maxContentSourceBytes, "-C", authority.repositoryRoot, "show", "--no-textconv", "--format=", fmt.Sprintf("%s:%s", source.Commit.Hex(), source.Path))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(body) {
		return "", &ValidationError{Reason: "content source must be valid UTF-8"}
	}
	return string(body), nil
}

func (a *gitAuthority) succeedWithEnvironment(ctx context.Context, maximum int, environment []string, arguments ...string) ([]byte, error) {
	result, err := a.runWithEnvironment(ctx, maximum, environment, arguments...)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, newGitError(gitFailureProcess)
	}
	return result.output, nil
}

func updateContentRef(ctx context.Context, authority *gitAuthority, durableRef string, id ObjectID, format ObjectFormat) error {
	current, err := authority.run(ctx, maxGitSelectionOutput, "-C", authority.repositoryRoot, "rev-parse", "--verify", "--quiet", "--end-of-options", durableRef+"^{commit}")
	if err != nil {
		return err
	}
	if current.exitCode == 0 {
		existing, parseErr := parseGitOID(format, bytes.TrimSpace(current.output))
		if parseErr != nil || !existing.Equal(id) {
			return &ValidationError{Reason: "content durable ref already names another commit"}
		}
		// Republish an exact replay under the current durability policy. Content
		// objects created by this writer were already fsynced; this refreshes a
		// retained ref before a legacy SQL body can be retired.
		return publishContentRef(ctx, authority, durableRef, id, id.Hex())
	}
	if current.exitCode != 1 {
		return newGitError(gitFailureProcess)
	}
	zero := strings.Repeat("0", format.OIDLength()*2)
	if err := publishContentRef(ctx, authority, durableRef, id, zero); err == nil {
		return nil
	}
	current, err = authority.run(ctx, maxGitSelectionOutput, "-C", authority.repositoryRoot, "rev-parse", "--verify", "--quiet", "--end-of-options", durableRef+"^{commit}")
	if err != nil || current.exitCode != 0 {
		if err != nil {
			return err
		}
		return newGitError(gitFailureProcess)
	}
	existing, parseErr := parseGitOID(format, bytes.TrimSpace(current.output))
	if parseErr != nil || !existing.Equal(id) {
		return &ValidationError{Reason: "content durable ref already names another commit"}
	}
	return nil
}

func publishContentRef(ctx context.Context, authority *gitAuthority, durableRef string, id ObjectID, expected string) error {
	return authority.rewriteConfig(ctx, maxGitSelectionOutput, contentWriteGitArguments(authority.repositoryRoot, "update-ref", "--no-deref", durableRef, id.Hex(), expected)...)
}

func regularContentTreeEntry(value []byte, file string) bool {
	entry, found := bytes.CutSuffix(value, []byte{0})
	if !found {
		return false
	}
	meta, name, found := bytes.Cut(entry, []byte{'\t'})
	if !found || string(name) != file {
		return false
	}
	fields := bytes.Fields(meta)
	return len(fields) == 3 && (string(fields[0]) == "100644" || string(fields[0]) == "100755") && string(fields[1]) == "blob"
}

func validContentRef(value string) bool {
	parts := strings.Split(value, "/")
	if len(parts) != 5 || parts[0] != "refs" || parts[1] != "dark-factory" || parts[2] != "content" || len(parts[3]) != 32 || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	_, idErr := hex.DecodeString(parts[3])
	revision, revisionErr := strconv.ParseUint(parts[4], 10, 64)
	return idErr == nil && revisionErr == nil && revision != 0
}
