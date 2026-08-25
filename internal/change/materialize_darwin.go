//go:build darwin

package change

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	directoryMode = 0o700
)

type fileIdentity struct {
	device uint64
	inode  uint64
}

func materialize(ctx context.Context, parent, target string, manifest Manifest, source BlobSource, hook materializeHook) (result MaterializeResult, returnErr error) {
	if err := validateMaterializeInput(parent, target, manifest, source); err != nil {
		return MaterializeResult{}, err
	}
	if err := checkpoint(ctx, hook, materializePoint{}); err != nil {
		return MaterializeResult{}, err
	}

	parentFD, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("open verified Change parent: %w", err)
	}
	defer unix.Close(parentFD)
	parentID, err := verifySecureParent(parentFD)
	if err != nil {
		return MaterializeResult{}, err
	}
	if err := verifyParentPath(parent, parentID); err != nil {
		return MaterializeResult{}, err
	}
	if exists, err := namedEntryExists(parentFD, target); err != nil {
		return MaterializeResult{}, err
	} else if exists {
		return MaterializeResult{}, &ConflictError{Target: changePath(parent, target)}
	}

	stagingName, stageFD, stageID, err := createStaging(parentFD)
	if err != nil {
		return MaterializeResult{}, err
	}
	defer unix.Close(stageFD)
	published := false
	defer func() {
		if returnErr == nil || published {
			return
		}
		point := materializePoint{step: stepBeforeOwnedStageCleanup, parent: parent, stagingName: stagingName, targetName: target}
		cleanupErr := checkpoint(context.Background(), hook, point)
		if cleanupErr == nil {
			cleanupErr = cleanupOwnedStage(parentFD, stagingName, stageFD, stageID)
		}
		returnErr = joinFailure(returnErr, cleanupErr)
	}()

	for _, entry := range manifest.entries {
		if err := materializeEntry(ctx, stageFD, stageID.device, entry, source, hook, parent, stagingName, target); err != nil {
			return MaterializeResult{}, err
		}
	}
	point := materializePoint{step: stepBeforeTreeVerify, parent: parent, stagingName: stagingName, targetName: target}
	if err := checkpoint(ctx, hook, point); err != nil {
		return MaterializeResult{}, err
	}
	actual, directories, err := scanTree(stageFD, stageID.device, manifest.format, manifest.base)
	if err != nil {
		return MaterializeResult{}, fmt.Errorf("verify staged Change: %w", err)
	}
	if !manifestsEqual(manifest, actual) || !slices.Equal(expectedDirectories(manifest), directories) {
		return MaterializeResult{}, &ValidationError{Reason: "staged tree differs from selected manifest"}
	}
	commitment := manifest.Commitment()
	if !commitment.Equal(actual.Commitment()) {
		return MaterializeResult{}, &ValidationError{Reason: "reconstructed tree commitment differs from selection"}
	}
	point.step = stepBeforeTreeFsync
	if err := checkpoint(ctx, hook, point); err != nil {
		return MaterializeResult{}, err
	}
	if err := fsyncTree(stageFD, stageID.device); err != nil {
		return MaterializeResult{}, fmt.Errorf("fsync staged Change: %w", err)
	}
	if err := verifyNamedIdentity(parentFD, stagingName, stageID); err != nil {
		return MaterializeResult{}, err
	}
	if err := verifyParentPath(parent, parentID); err != nil {
		return MaterializeResult{}, err
	}
	point.step = stepBeforeRename
	if err := checkpoint(ctx, hook, point); err != nil {
		return MaterializeResult{}, err
	}
	if err := unix.RenameatxNp(parentFD, stagingName, parentFD, target, unix.RENAME_EXCL); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return MaterializeResult{}, &ConflictError{Target: changePath(parent, target)}
		}
		return MaterializeResult{}, fmt.Errorf("publish Change without replacement: %w", err)
	}
	published = true

	unknown := func(cause error) (MaterializeResult, error) {
		return MaterializeResult{}, &OutcomeUnknownError{
			Target:     changePath(parent, target),
			Commitment: commitment,
			Device:     stageID.device,
			Inode:      stageID.inode,
			Cause:      cause,
		}
	}
	point.step = stepAfterRename
	if err := checkpoint(ctx, hook, point); err != nil {
		return unknown(err)
	}
	if err := verifyNamedIdentity(parentFD, target, stageID); err != nil {
		return unknown(err)
	}
	if err := verifyParentPath(parent, parentID); err != nil {
		return unknown(err)
	}
	postPublication, postDirectories, err := scanTree(stageFD, stageID.device, manifest.format, manifest.base)
	if err != nil {
		return unknown(fmt.Errorf("reconstruct published Change: %w", err))
	}
	if !manifestsEqual(manifest, postPublication) || !slices.Equal(expectedDirectories(manifest), postDirectories) || !commitment.Equal(postPublication.Commitment()) {
		return unknown(&ValidationError{Reason: "published tree differs from selected manifest"})
	}
	point.step = stepBeforeParentFsync
	if err := checkpoint(ctx, hook, point); err != nil {
		return unknown(err)
	}
	if err := unix.Fsync(parentFD); err != nil {
		return unknown(fmt.Errorf("fsync Change parent after publication: %w", err))
	}
	return MaterializeResult{
		path:       changePath(parent, target),
		commitment: commitment,
		device:     stageID.device,
		inode:      stageID.inode,
		fileCount:  manifest.FileCount(),
		blobBytes:  manifest.BlobBytes(),
	}, nil
}

func validateMaterializeInput(parent, target string, manifest Manifest, source BlobSource) error {
	if !filepath.IsAbs(parent) {
		return &ValidationError{Reason: "Change parent must be absolute"}
	}
	if source == nil {
		return &ValidationError{Reason: "blob source is required"}
	}
	if !manifest.format.valid() || !manifest.base.format.valid() || manifest.base.format != manifest.format {
		return &ValidationError{Reason: "invalid manifest"}
	}
	if target == "" || target == "." || target == ".." || len(target) > maxNameBytes || !utf8.ValidString(target) || strings.ContainsAny(target, "/\x00") {
		return &ValidationError{Reason: "target must be one safe name"}
	}
	if strings.EqualFold(target, ".git") {
		return &ValidationError{Reason: ".git target is forbidden"}
	}
	return nil
}

func checkpoint(ctx context.Context, hook materializeHook, point materializePoint) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if hook != nil {
		if err := hook(point); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func verifySecureParent(fd int) (fileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fileIdentity{}, fmt.Errorf("stat Change parent: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != directoryMode || stat.Uid != uint32(os.Geteuid()) {
		return fileIdentity{}, &ValidationError{Reason: "Change parent must be an owned mode-0700 directory"}
	}
	return identityOf(stat), nil
}

func verifyParentPath(path string, expected fileIdentity) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return fmt.Errorf("reopen Change parent without symlinks: %w", err)
	}
	defer unix.Close(fd)
	actual, err := verifySecureParent(fd)
	if err != nil {
		return err
	}
	if actual != expected {
		return &UnresolvedError{Stage: "verified parent identity", Cause: errors.New("Change parent identity changed")}
	}
	return nil
}

func identityOf(stat unix.Stat_t) fileIdentity {
	return fileIdentity{device: uint64(stat.Dev), inode: stat.Ino}
}

func namedEntryExists(parentFD int, name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return false, fmt.Errorf("inspect Change target: %w", err)
}

func createStaging(parentFD int) (string, int, fileIdentity, error) {
	for attempt := 0; attempt < 32; attempt++ {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", -1, fileIdentity{}, fmt.Errorf("generate staging identity: %w", err)
		}
		name := ".dark-factory-change-" + hex.EncodeToString(random[:])
		if err := unix.Mkdirat(parentFD, name, directoryMode); err != nil {
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", -1, fileIdentity{}, fmt.Errorf("create Change staging directory: %w", err)
		}
		fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			return "", -1, fileIdentity{}, &UnresolvedError{Stage: "open new staging directory", Cause: err}
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			unix.Close(fd)
			return "", -1, fileIdentity{}, &UnresolvedError{Stage: "stat new staging directory", Cause: err}
		}
		if err := unix.Fchmod(fd, directoryMode); err != nil {
			unix.Close(fd)
			return "", -1, fileIdentity{}, &UnresolvedError{Stage: "secure new staging directory", Cause: err}
		}
		if err := unix.Fstat(fd, &stat); err != nil {
			unix.Close(fd)
			return "", -1, fileIdentity{}, &UnresolvedError{Stage: "restat new staging directory", Cause: err}
		}
		id := identityOf(stat)
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != directoryMode {
			unix.Close(fd)
			return "", -1, fileIdentity{}, &UnresolvedError{Stage: "new staging directory mode", Cause: errors.New("unexpected staging directory")}
		}
		if err := verifyNamedIdentity(parentFD, name, id); err != nil {
			unix.Close(fd)
			return "", -1, fileIdentity{}, err
		}
		return name, fd, id, nil
	}
	return "", -1, fileIdentity{}, errors.New("could not allocate unique Change staging directory")
}

func verifyNamedIdentity(parentFD int, name string, expected fileIdentity) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &UnresolvedError{Stage: "named filesystem identity", Cause: err}
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || identityOf(stat) != expected {
		return &UnresolvedError{Stage: "named filesystem identity", Cause: errors.New("directory identity changed")}
	}
	return nil
}

func materializeEntry(ctx context.Context, rootFD int, rootDevice uint64, entry Entry, source BlobSource, hook materializeHook, parent, staging, target string) error {
	data, err := source(ctx, entry.oid)
	if err != nil {
		return fmt.Errorf("read selected blob %s: %w", entry.oid.Hex(), err)
	}
	if uint64(len(data)) != entry.size {
		return &ValidationError{Reason: fmt.Sprintf("blob size differs for %q", entry.path)}
	}
	if !hashBlob(entry.oid.format, data).equal(entry.oid) {
		return &ValidationError{Reason: fmt.Sprintf("blob object ID differs for %q", entry.path)}
	}
	components := strings.Split(string(entry.path), "/")
	point := materializePoint{step: stepBeforeEntryParentOpen, parent: parent, stagingName: staging, targetName: target, entryPath: bytes.Clone(entry.path)}
	if err := checkpoint(ctx, hook, point); err != nil {
		return err
	}
	parentFD, closeParent, err := openEntryParent(rootFD, rootDevice, components[:len(components)-1])
	if err != nil {
		return err
	}
	if closeParent {
		defer unix.Close(parentFD)
	}
	point.step = stepBeforeFileCreate
	if err := checkpoint(ctx, hook, point); err != nil {
		return err
	}
	permissions := uint32(0o644)
	if entry.mode == "100755" {
		permissions = 0o755
	}
	fd, err := unix.Openat(parentFD, components[len(components)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, permissions)
	if err != nil {
		return fmt.Errorf("create staged file %q: %w", entry.path, err)
	}
	defer unix.Close(fd)
	if err := unix.Fchmod(fd, permissions); err != nil {
		return fmt.Errorf("set staged file mode %q: %w", entry.path, err)
	}
	point.step = stepBeforeFileWrite
	if err := checkpoint(ctx, hook, point); err != nil {
		return err
	}
	for written := 0; written < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(written+(1<<20), len(data))
		n, err := unix.Write(fd, data[written:end])
		if err != nil {
			return fmt.Errorf("write staged file %q: %w", entry.path, err)
		}
		if n == 0 {
			return fmt.Errorf("write staged file %q: %w", entry.path, io.ErrShortWrite)
		}
		written += n
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat staged file %q: %w", entry.path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || uint32(stat.Mode&0o777) != permissions || stat.Nlink != 1 || stat.Size != int64(entry.size) || uint64(stat.Dev) != rootDevice {
		return &ValidationError{Reason: fmt.Sprintf("staged file identity differs for %q", entry.path)}
	}
	point.step = stepBeforeFileFsync
	if err := checkpoint(ctx, hook, point); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return fmt.Errorf("fsync staged file %q: %w", entry.path, err)
	}
	return nil
}

func openEntryParent(rootFD int, rootDevice uint64, components []string) (int, bool, error) {
	current := rootFD
	owned := false
	for _, component := range components {
		if err := unix.Mkdirat(current, component, directoryMode); err != nil && !errors.Is(err, unix.EEXIST) {
			if owned {
				unix.Close(current)
			}
			return -1, false, fmt.Errorf("create staged directory %q: %w", component, err)
		}
		next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			if owned {
				unix.Close(current)
			}
			return -1, false, fmt.Errorf("open staged directory %q without following: %w", component, err)
		}
		var stat unix.Stat_t
		err = unix.Fstat(next, &stat)
		if err == nil && (stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != directoryMode || uint64(stat.Dev) != rootDevice) {
			err = errors.New("staged directory identity or mode differs")
		}
		if owned {
			unix.Close(current)
		}
		if err != nil {
			unix.Close(next)
			return -1, false, fmt.Errorf("verify staged directory %q: %w", component, err)
		}
		current = next
		owned = true
	}
	return current, owned, nil
}

func scanTree(rootFD int, rootDevice uint64, format ObjectFormat, base ObjectID) (Manifest, []string, error) {
	entries := make([]Entry, 0)
	directories := make([]string, 0)
	var total uint64
	if err := scanDirectory(rootFD, rootDevice, format, "", &entries, &directories, &total); err != nil {
		return Manifest{}, nil, err
	}
	manifest, err := NewManifest(format, base, entries)
	if err != nil {
		return Manifest{}, nil, err
	}
	slices.Sort(directories)
	return manifest, directories, nil
}

func scanDirectory(dirFD int, rootDevice uint64, format ObjectFormat, prefix string, entries *[]Entry, directories *[]string, total *uint64) error {
	names, err := directoryNames(dirFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		path := name
		if prefix != "" {
			path = prefix + "/" + name
		}
		if err := validatePath([]byte(path)); err != nil {
			return err
		}
		var before unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if uint64(before.Dev) != rootDevice {
			return &ValidationError{Reason: "staged entry crosses filesystem device"}
		}
		switch before.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if before.Mode&0o777 != directoryMode {
				return &ValidationError{Reason: "staged directory mode differs"}
			}
			childFD, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			err = unix.Fstat(childFD, &opened)
			if err == nil && identityOf(opened) != identityOf(before) {
				err = errors.New("staged directory changed while opening")
			}
			if err == nil {
				*directories = append(*directories, path)
				err = scanDirectory(childFD, rootDevice, format, path, entries, directories, total)
			}
			unix.Close(childFD)
			if err != nil {
				return err
			}
		case unix.S_IFREG:
			if before.Nlink != 1 {
				return &ValidationError{Reason: "hard-linked staged file forbidden"}
			}
			if before.Size < 0 || uint64(before.Size) > maxBlobBytes || *total > maxTotalBytes-uint64(before.Size) {
				return &LimitError{Reason: "reconstructed byte limit exceeded"}
			}
			permissions := before.Mode & 0o777
			mode := "100644"
			if permissions == 0o755 {
				mode = "100755"
			} else if permissions != 0o644 {
				return &ValidationError{Reason: "staged file mode differs"}
			}
			fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			err = unix.Fstat(fd, &opened)
			if err == nil && (identityOf(opened) != identityOf(before) || opened.Mode&unix.S_IFMT != unix.S_IFREG || opened.Nlink != 1) {
				err = errors.New("staged file changed while opening")
			}
			var oid ObjectID
			if err == nil {
				oid, err = hashOpenBlob(format, fd, uint64(opened.Size))
			}
			unix.Close(fd)
			if err != nil {
				return err
			}
			entry, err := NewEntry([]byte(path), mode, uint64(opened.Size), oid)
			if err != nil {
				return err
			}
			*entries = append(*entries, entry)
			*total += uint64(opened.Size)
			if len(*entries) > maxFileCount {
				return &LimitError{Reason: "reconstructed file count exceeded"}
			}
		default:
			return &ValidationError{Reason: "non-regular staged entry forbidden"}
		}
	}
	return nil
}

func hashOpenBlob(format ObjectFormat, fd int, size uint64) (ObjectID, error) {
	hasher := format.newHash()
	_, _ = fmt.Fprintf(hasher, "blob %d\x00", size)
	readFD, err := unix.Dup(fd)
	if err != nil {
		return ObjectID{}, err
	}
	file := os.NewFile(uintptr(readFD), "staged blob")
	if file == nil {
		unix.Close(readFD)
		return ObjectID{}, errors.New("wrap staged blob descriptor")
	}
	if _, err := io.Copy(hasher, file); err != nil {
		file.Close()
		return ObjectID{}, err
	}
	if err := file.Close(); err != nil {
		return ObjectID{}, err
	}
	return NewObjectID(format, hasher.Sum(nil))
}

func directoryNames(dirFD int) ([]string, error) {
	fd, err := unix.Openat(dirFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "Change directory")
	if file == nil {
		unix.Close(fd)
		return nil, errors.New("wrap Change directory descriptor")
	}
	entries, err := file.ReadDir(-1)
	closeErr := file.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	slices.Sort(names)
	return names, nil
}

func fsyncTree(dirFD int, rootDevice uint64) error {
	names, err := directoryNames(dirFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		var stat unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		childFD, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			return err
		}
		var opened unix.Stat_t
		err = unix.Fstat(childFD, &opened)
		if err == nil && (identityOf(opened) != identityOf(stat) || uint64(opened.Dev) != rootDevice) {
			err = errors.New("staged directory changed while syncing")
		}
		if err == nil {
			err = fsyncTree(childFD, rootDevice)
		}
		unix.Close(childFD)
		if err != nil {
			return err
		}
	}
	return unix.Fsync(dirFD)
}

func expectedDirectories(manifest Manifest) []string {
	seen := make(map[string]struct{})
	for _, entry := range manifest.entries {
		components := strings.Split(string(entry.path), "/")
		for i := 1; i < len(components); i++ {
			seen[strings.Join(components[:i], "/")] = struct{}{}
		}
	}
	directories := make([]string, 0, len(seen))
	for directory := range seen {
		directories = append(directories, directory)
	}
	slices.Sort(directories)
	return directories
}

func manifestsEqual(left, right Manifest) bool {
	if left.format != right.format || !left.base.equal(right.base) || left.blobBytes != right.blobBytes || len(left.entries) != len(right.entries) {
		return false
	}
	for i := range left.entries {
		a, b := left.entries[i], right.entries[i]
		if !bytes.Equal(a.path, b.path) || a.mode != b.mode || a.size != b.size || !a.oid.equal(b.oid) {
			return false
		}
	}
	return true
}

func cleanupOwnedStage(parentFD int, stagingName string, stageFD int, expected fileIdentity) error {
	if err := verifyNamedIdentity(parentFD, stagingName, expected); err != nil {
		return err
	}
	if err := removeDirectoryContents(stageFD, expected.device); err != nil {
		return err
	}
	if err := verifyNamedIdentity(parentFD, stagingName, expected); err != nil {
		return err
	}
	if err := unix.Unlinkat(parentFD, stagingName, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove owned staging directory: %w", err)
	}
	if err := unix.Fsync(parentFD); err != nil {
		return fmt.Errorf("fsync parent after staging cleanup: %w", err)
	}
	return nil
}

func removeDirectoryContents(dirFD int, rootDevice uint64) error {
	names, err := directoryNames(dirFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		var before unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if uint64(before.Dev) != rootDevice {
			return errors.New("refuse cleanup across filesystem device")
		}
		if before.Mode&unix.S_IFMT == unix.S_IFDIR {
			childFD, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			err = unix.Fstat(childFD, &opened)
			if err == nil && identityOf(opened) != identityOf(before) {
				err = errors.New("directory changed during owned cleanup")
			}
			if err == nil {
				err = removeDirectoryContents(childFD, rootDevice)
			}
			unix.Close(childFD)
			if err != nil {
				return err
			}
			var after unix.Stat_t
			if err := unix.Fstatat(dirFD, name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil || identityOf(after) != identityOf(before) {
				if err == nil {
					err = errors.New("directory changed before owned removal")
				}
				return err
			}
			if err := unix.Unlinkat(dirFD, name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
			continue
		}
		var after unix.Stat_t
		if err := unix.Fstatat(dirFD, name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil || identityOf(after) != identityOf(before) || after.Mode&unix.S_IFMT != before.Mode&unix.S_IFMT {
			if err == nil {
				err = errors.New("entry changed before owned removal")
			}
			return err
		}
		if err := unix.Unlinkat(dirFD, name, 0); err != nil {
			return err
		}
	}
	return nil
}
