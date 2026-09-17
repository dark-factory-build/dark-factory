//go:build darwin

package change

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// IsolateWorktree moves a quiescent retained Change to private Git
// administration without checking out, resetting, or replacing source files.
// Its original refs, registration, and index remain available for recovery.
func IsolateWorktree(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, path, branch string, head ObjectID) (WorktreeFacts, error) {
	a, err := openGitAuthority(gitExecutable, repositoryRoot, expected, nil, true)
	if err != nil {
		return WorktreeFacts{}, err
	}
	defer a.close()
	facts, err := a.verifyWorktree(ctx, path, branch, head)
	private := GitDirectoryForChange(repositoryRoot, path)
	originPath := filepath.Join(filepath.Dir(private), "isolation-origin")
	if err != nil {
		return facts, err
	}
	alreadyPrivate := facts.GitDirectory() == private
	if alreadyPrivate {
		if _, err := os.Lstat(originPath); errors.Is(err, os.ErrNotExist) {
			return facts, nil
		}
	}
	if err := preparePrivateGitParent(repositoryRoot, path); err != nil {
		return WorktreeFacts{}, err
	}
	// The kernel lease also serializes recovery of our marked Git lock files.
	fd, err := unix.Open(filepath.Join(filepath.Dir(private), "isolation.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0o600)
	if err != nil {
		return WorktreeFacts{}, err
	}
	defer unix.Close(fd)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return WorktreeFacts{}, err
	}
	gitfile := filepath.Join(path, ".git")
	if alreadyPrivate {
		// A death immediately after the atomic pointer swap may leave our old
		// Git locks behind. The kernel lease proves no migration still owns them.
		old, err := privateGitfileTarget(originPath)
		if err != nil || filepath.Dir(old) != filepath.Join(repositoryRoot, ".git", "worktrees") {
			return WorktreeFacts{}, &ValidationError{Reason: "private Git migration origin is invalid"}
		}
		originFD, err := unix.Open(old, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
		if err != nil {
			return WorktreeFacts{}, err
		}
		_, backlink, readErr := readGitAdminFile(originFD, "gitdir", maxGitSelectionOutput)
		_ = unix.Close(originFD)
		if readErr != nil || strings.TrimSpace(string(backlink)) != gitfile {
			return WorktreeFacts{}, &ValidationError{Reason: "private Git migration origin belongs to another worktree"}
		}
		for _, lock := range []string{filepath.Join(old, "index.lock"), filepath.Join(repositoryRoot, ".git", "refs", "heads", branch) + ".lock"} {
			data, err := os.ReadFile(lock)
			if err == nil && string(data) == "dark-factory-isolation "+old+"\n" {
				if err := os.Remove(lock); err != nil {
					return WorktreeFacts{}, err
				}
			}
		}
		return facts, nil
	}
	original, err := os.ReadFile(gitfile)
	if err != nil {
		return WorktreeFacts{}, err
	}
	old, err := privateGitfileTarget(gitfile)
	if err != nil || filepath.Dir(old) != filepath.Join(repositoryRoot, ".git", "worktrees") {
		return WorktreeFacts{}, &ValidationError{Reason: "retained worktree registration changed"}
	}
	// Provenance is outside the worker-writable private admin. It can never
	// turn worker-authored metadata into authority over legacy Git locks.
	if previous, err := os.ReadFile(originPath); err == nil {
		if !bytes.Equal(previous, original) {
			return WorktreeFacts{}, &ValidationError{Reason: "Git migration origin changed"}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeFacts{}, err
	} else if err := writeSyncedGitFile(originPath, original); err != nil {
		return WorktreeFacts{}, err
	}
	if err := syncGitDirectory(filepath.Dir(originPath)); err != nil {
		return WorktreeFacts{}, err
	}
	for _, lock := range []string{filepath.Join(old, "index.lock"), filepath.Join(repositoryRoot, ".git", "refs", "heads", branch) + ".lock"} {
		if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
			return WorktreeFacts{}, err
		}
		marker := []byte("dark-factory-isolation " + old + "\n")
		file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			data, readErr := os.ReadFile(lock)
			if readErr != nil || !bytes.Equal(data, marker) {
				return WorktreeFacts{}, &ValidationError{Reason: "retained worktree has an active Git lock"}
			}
		} else if err != nil {
			return WorktreeFacts{}, err
		} else {
			_, writeErr := file.Write(marker)
			if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
				return WorktreeFacts{}, err
			}
		}
		defer os.Remove(lock)
	}
	if _, err := a.verifyWorktree(ctx, path, branch, head); err != nil {
		return WorktreeFacts{}, err
	}
	registration := filepath.Join(private, "worktrees", filepath.Base(path))
	if _, err := os.Lstat(private); errors.Is(err, os.ErrNotExist) {
		stage, err := os.MkdirTemp(filepath.Dir(private), "isolation-")
		if err != nil {
			return WorktreeFacts{}, err
		}
		// Incomplete preparations remain private for diagnosis; retries never
		// remove unknown source, registrations or objects to make progress.
		// ponytail: a local bare clone copies all objects, including dangling
		// operation-state objects. Cost scales with repository storage; a
		// future narrower transfer must prove every retained metadata root.
		if _, err := a.succeed(ctx, maxGitSelectionOutput, "-c", "protocol.file.allow=always", "clone", "--bare", "--local", "--no-hardlinks", repositoryRoot, stage); err != nil {
			return WorktreeFacts{}, err
		}
		if _, err := a.succeed(ctx, maxGitSelectionOutput, "--git-dir", stage, "config", "--remove-section", "remote.origin"); err != nil {
			return WorktreeFacts{}, err
		}
		if _, err := a.succeed(ctx, maxGitSelectionOutput, "--git-dir", stage, "update-ref", "refs/heads/"+branch, head.Hex()); err != nil {
			return WorktreeFacts{}, err
		}
		stagedRegistration := filepath.Join(stage, "worktrees", filepath.Base(path))
		if err := copyGitAdministration(old, stagedRegistration); err != nil {
			return WorktreeFacts{}, err
		}
		for name, data := range map[string][]byte{"commondir": []byte("../..\n"), "gitdir": []byte(gitfile + "\n")} {
			if err := writeSyncedGitFile(filepath.Join(stagedRegistration, name), data); err != nil {
				return WorktreeFacts{}, err
			}
		}
		if err := syncGitAdministration(stage); err != nil {
			return WorktreeFacts{}, err
		}
		if err := os.Rename(stage, private); err != nil {
			return WorktreeFacts{}, err
		}
		if err := syncGitDirectory(filepath.Dir(private)); err != nil {
			return WorktreeFacts{}, err
		}
	} else if err != nil {
		return WorktreeFacts{}, err
	}
	// A crash after preparation but before the pointer swap resumes only from
	// this exact original registration and byte-identical retained metadata.
	provenance, err := os.ReadFile(originPath)
	if err != nil || !bytes.Equal(provenance, original) {
		return WorktreeFacts{}, &ValidationError{Reason: "private Git preparation belongs to different source"}
	}
	if err := validatePrivateGitAdmin(private); err != nil {
		return WorktreeFacts{}, err
	}
	before, err := gitAdministrationDigest(old)
	if err != nil {
		return WorktreeFacts{}, err
	}
	after, err := gitAdministrationDigest(registration)
	if err != nil || before != after {
		return WorktreeFacts{}, &ValidationError{Reason: "retained Git metadata changed during isolation"}
	}
	if _, err := a.succeed(ctx, maxGitSelectionOutput, "--git-dir", registration, "--work-tree", path, "fsck", "--connectivity-only", "--no-dangling"); err != nil {
		return WorktreeFacts{}, err
	}
	if _, err := a.verifyWorktree(ctx, path, branch, head); err != nil {
		return WorktreeFacts{}, err
	}
	current, err := os.ReadFile(gitfile)
	if err != nil || !bytes.Equal(current, original) {
		return WorktreeFacts{}, &ValidationError{Reason: "retained Gitfile changed during isolation"}
	}
	replacement, err := os.CreateTemp(filepath.Dir(private), "gitfile-")
	if err != nil {
		return WorktreeFacts{}, err
	}
	defer os.Remove(replacement.Name())
	_, writeErr := replacement.WriteString("gitdir: " + registration + "\n")
	if err := errors.Join(writeErr, replacement.Sync(), replacement.Close()); err != nil {
		return WorktreeFacts{}, err
	}
	if err := os.Rename(replacement.Name(), gitfile); err != nil {
		return WorktreeFacts{}, err
	}
	if err := syncGitDirectory(path); err != nil {
		return WorktreeFacts{}, err
	}
	return a.verifyWorktree(ctx, path, branch, head)
}

func gitAdministrationDigest(root string) ([32]byte, error) {
	digest := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "index.lock" || relative == "commondir" || relative == "gitdir" {
			return nil // Migration owns the lock and intentionally rewrites these pointers.
		}
		io.WriteString(digest, relative+"\x00")
		if entry.IsDir() {
			io.WriteString(digest, "directory\x00")
			return nil
		}
		if !entry.Type().IsRegular() || strings.HasSuffix(entry.Name(), ".lock") {
			return &ValidationError{Reason: "retained Git metadata is not quiescent"}
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		content := sha256.New()
		_, readErr := io.Copy(content, file)
		io.WriteString(digest, "file\x00")
		digest.Write(content.Sum(nil))
		return errors.Join(readErr, file.Close())
	})
	var result [32]byte
	copy(result[:], digest.Sum(nil))
	return result, err
}

func copyGitAdministration(source, target string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "index.lock" {
			return nil // The caller holds this exact migration lock.
		}
		if strings.HasSuffix(entry.Name(), ".lock") || entry.Type()&os.ModeSymlink != 0 {
			return &ValidationError{Reason: "retained Git administration has a lock or symlink"}
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		if !entry.Type().IsRegular() {
			return &ValidationError{Reason: "retained Git administration has a special file"}
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		return errors.Join(copyErr, output.Close())
	})
}

func writeSyncedGitFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}

func syncGitAdministration(path string) error {
	return filepath.WalkDir(path, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return syncGitDirectory(path)
	})
}

func syncGitDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
