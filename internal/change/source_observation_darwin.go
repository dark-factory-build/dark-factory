//go:build darwin

package change

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ArchiveSource reads the configured target without fetching, changing refs,
// executing filters, or consulting the checkout's working files.
func ArchiveSource(ctx context.Context, git, root, target string, expected RepositorySourceIdentity) (string, []byte, error) {
	if err := validateRevision(target); err != nil {
		return "", nil, err
	}
	a, err := openGitAuthority(git, root, expected.Root, nil, true)
	if err != nil {
		return "", nil, err
	}
	defer a.close()
	actual, err := a.sourceIdentity(ctx)
	if err != nil {
		return "", nil, err
	}
	if actual.Root != expected.Root || actual.Git != expected.Git || actual.OriginDigest != expected.OriginDigest {
		err = &ValidationError{Reason: "registered source identity changed"}
		return "", nil, err
	}
	revision, err := a.succeed(ctx, 256, "-C", root, "rev-parse", "--verify", "--end-of-options", target+"^{commit}")
	if err != nil {
		return "", nil, err
	}
	head := strings.TrimSpace(string(revision))
	archive, err := a.succeed(ctx, 64<<20, "-C", root, "-c", "tar.umask=0077", "archive", "--format=tar", head)
	if err != nil {
		return "", nil, err
	}
	tree, err := a.succeed(ctx, 4<<20, "-C", root, "ls-tree", "-r", "-z", head)
	if err != nil {
		return "", nil, err
	}
	if err := verifySourceArchive(tree, archive, len(head)); err != nil {
		return "", nil, err
	}
	return head, archive, nil
}

// ObserveSource reads a bounded diff in an already registered repository or
// validated Change worktree. A dirty result is distinct from its underlying HEAD.
func ObserveSource(ctx context.Context, git, root, worktree, base, head string, expected RepositorySourceIdentity) (SourceObservation, error) {
	result := SourceObservation{Base: base, Head: head, Kind: "committed", Paths: []SourcePath{}}
	for _, revision := range []string{base, head} {
		if revision == "" && worktree != "" {
			continue
		}
		if data, err := hex.DecodeString(revision); err != nil || (len(data) != 20 && len(data) != 32) || strings.ToLower(revision) != revision {
			return result, &ValidationError{Reason: "source revisions must be full object IDs"}
		}
	}
	a, err := openGitAuthority(git, root, expected.Root, nil, true)
	if err != nil {
		return result, err
	}
	defer a.close()
	actual, err := a.sourceIdentity(ctx)
	if err != nil {
		return result, err
	}
	if actual.Root != expected.Root || actual.Git != expected.Git || actual.OriginDigest != expected.OriginDigest {
		return result, &ValidationError{Reason: "registered source identity changed"}
	}
	where := root
	if worktree != "" {
		facts, err := a.inspectWorktree(ctx, worktree)
		if err != nil {
			return result, err
		}
		if head != "" && facts.Head().Hex() != head {
			return result, &ValidationError{Reason: "observed head changed"}
		}
		where = worktree
		head = facts.Head().Hex()
		result.Head = head
		if facts.Dirty() {
			result.Kind = "working-tree"
		}
	}
	// A PR target can advance independently. Compare from its actual common
	// ancestor so those integrated edits never appear as proposed removals.
	common, err := a.succeed(ctx, 256, "-C", where, "merge-base", "--all", base, head)
	if err != nil {
		return result, err
	}
	comparison := strings.TrimSpace(string(common))
	if raw, err := hex.DecodeString(comparison); err != nil || len(raw)*2 != len(base) {
		return result, &ValidationError{Reason: "comparison base is ambiguous"}
	}
	result.Target = base
	result.Base = comparison
	base = comparison
	arguments := []string{"-C", where, "diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "--find-renames", "--name-status", "-z", base}
	if result.Kind == "committed" {
		arguments = append(arguments, head)
	}
	arguments = append(arguments, "--")
	raw, err := a.succeed(ctx, 4<<20, arguments...)
	if err != nil {
		return result, err
	}
	entries := bytes.Split(raw, []byte{0})
	for i := 0; i+1 < len(entries); {
		status := string(entries[i])
		i++
		file := string(entries[i])
		i++
		item := SourcePath{Path: file}
		switch status[0] {
		case 'A':
			item.Status = "added"
		case 'D':
			item.Status = "deleted"
		case 'M', 'T':
			item.Status = "modified"
		case 'R':
			if i >= len(entries)-1 {
				return result, newGitError(gitFailureProtocol)
			}
			item.Status, item.OldPath, item.Path = "renamed", file, string(entries[i])
			i++
		default:
			return result, newGitError(gitFailureProtocol)
		}
		if validateContentPath(item.Path) != nil || item.OldPath != "" && validateContentPath(item.OldPath) != nil {
			return result, newGitError(gitFailureProtocol)
		}
		if len(result.Paths) < 32 {
			result.Paths = append(result.Paths, item)
		} else {
			result.Omitted++
		}
	}
	if result.Kind == "working-tree" {
		// The fingerprint includes contents, including untracked ordinary files.
		// It identifies this bounded observation; it is never a reviewed commit.
		fingerprintArgs := []string{"-C", where, "diff", "--binary", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", base, "--"}
		patch, err := a.succeed(ctx, 8<<20, fingerprintArgs...)
		if err != nil {
			return result, err
		}
		hash := sha256.New()
		hash.Write(patch)
		untracked, err := a.succeed(ctx, 1<<20, "-C", where, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return result, err
		}
		hash.Write(untracked)
		fs, err := os.OpenRoot(where)
		if err != nil {
			return result, newGitError(gitFailurePrivateIO)
		}
		defer fs.Close()
		for _, rawPath := range bytes.Split(untracked, []byte{0}) {
			if len(rawPath) == 0 {
				continue
			}
			file := string(rawPath)
			if validateContentPath(file) != nil {
				return result, newGitError(gitFailureProtocol)
			}
			info, err := fs.Lstat(filepath.FromSlash(file))
			if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
				return result, &ValidationError{Reason: "untracked source cannot be observed completely"}
			}
			opened, err := fs.Open(filepath.FromSlash(file))
			if err != nil {
				return result, newGitError(gitFailurePrivateIO)
			}
			body, err := io.ReadAll(io.LimitReader(opened, (1<<20)+1))
			opened.Close()
			if err != nil || len(body) > 1<<20 {
				return result, newGitError(gitFailurePrivateIO)
			}
			hash.Write(body)
			if len(result.Paths) < 32 {
				result.Paths = append(result.Paths, SourcePath{Status: "added", Path: file})
			} else {
				result.Omitted++
			}
		}
		result.Fingerprint = hex.EncodeToString(hash.Sum(nil))
		again, err := a.succeed(ctx, 8<<20, fingerprintArgs...)
		if err != nil || !bytes.Equal(patch, again) {
			return result, &ValidationError{Reason: "source changed during observation"}
		}
	}
	return result, nil
}

// Git archive honors export-ignore/export-subst attributes. Refuse an altered
// archive rather than labeling an incomplete export as the integrated tree.
func verifySourceArchive(tree, archive []byte, oidLength int) error {
	expected := map[string]string{}
	for _, entry := range bytes.Split(tree, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		metadata, name, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 {
			return newGitError(gitFailureProtocol)
		}
		if fields[0] == "100644" || fields[0] == "100755" {
			expected[name] = fields[2]
		}
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		item, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return newGitError(gitFailureProtocol)
		}
		if item.Typeflag != tar.TypeReg {
			continue
		}
		hash := sha256.New()
		if oidLength == 40 {
			hash = sha1.New()
		}
		fmt.Fprintf(hash, "blob %d%c", item.Size, 0)
		if _, err := io.Copy(hash, reader); err != nil {
			return newGitError(gitFailureProtocol)
		}
		if expected[item.Name] != hex.EncodeToString(hash.Sum(nil)) {
			return &ValidationError{Reason: "archive differs from integrated source"}
		}
		delete(expected, item.Name)
	}
	if len(expected) > 0 {
		return &ValidationError{Reason: "archive omits integrated source"}
	}
	return nil
}
