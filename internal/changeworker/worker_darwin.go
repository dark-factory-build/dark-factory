//go:build darwin

package changeworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

const shellPath = "/bin/sh"

type runtimeAuthority struct {
	root, namedRoot, config, home, temp, token *os.File
	runtimePath                                string
	rootID                                     runner.FileIdentity
	configID, homeID, tempID, tokenID          runner.FileIdentity
	configSize                                 int64
	configDigest                               [32]byte
}

func runShell(ctx context.Context) (resultErr error) {
	control, err := runner.OpenWorkerControl()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, control.Close()) }()
	runtimeDir, err := control.DuplicateRuntimeDirectory(ctx)
	if err != nil {
		return err
	}
	authority, config, err := openRuntimeAuthority(ctx, runtimeDir)
	if err != nil {
		_ = runtimeDir.Close()
		return err
	}
	authority.root = runtimeDir
	defer func() { resultErr = errors.Join(resultErr, authority.close()) }()

	selection, err := change.SelectGit(ctx, config.GitExecutable, config.RepositoryRoot, config.Revision, config.RepositoryIdentity)
	if err != nil {
		return err
	}
	selectionBytes, err := EncodeSelectionReport(SelectionReport{
		Format: selection.ObjectFormat(), Base: selection.Base(), Commitment: selection.Commitment(),
		EntryCount: selection.EntryCount(), BlobBytes: selection.BlobBytes(), Repository: selection.RepositoryIdentity(),
	})
	if err != nil || control.ReportSelection(selectionBytes) != nil {
		return ErrWorker
	}
	if err := control.AwaitPreparation(); err != nil {
		return err
	}

	prepared, err := change.Prepare(ctx, config.ChangeParent, config.FinalName, config.StagingName)
	if err != nil {
		return err
	}
	preparedOpen := true
	defer func() {
		if preparedOpen {
			resultErr = errors.Join(resultErr, prepared.Close())
		}
	}()
	preparationBytes, err := EncodePreparationReport(PreparationReport{Stage: prepared.Identity()})
	if err != nil || control.ReportPreparation(preparationBytes) != nil {
		return ErrWorker
	}
	if err := control.AwaitPopulation(); err != nil {
		return err
	}

	blobs, err := change.OpenGitBlobs(ctx, config.GitExecutable, config.RepositoryRoot, selection)
	if err != nil {
		return err
	}
	blobsOpen := true
	defer func() {
		if blobsOpen {
			resultErr = errors.Join(resultErr, blobs.Abort())
		}
	}()
	published, err := prepared.PopulateAndPublish(ctx, selection.Manifest(), blobs.Read)
	if err != nil {
		return err
	}
	if err := blobs.Close(); err != nil {
		blobsOpen = false
		return err
	}
	blobsOpen = false
	if err := prepared.Close(); err != nil {
		preparedOpen = false
		return err
	}
	preparedOpen = false
	facts := published.Facts()
	populationBytes, err := EncodePopulationReport(PopulationReport{
		Identity: facts.Identity(), Commitment: facts.Commitment(), EntryCount: facts.EntryCount(), BlobBytes: facts.BlobBytes(),
	})
	group := runner.ObserveProcessGroup(control.Identity())
	if group.Presence != runner.Present || len(group.Members) != 1 || group.Members[0] != control.Identity() {
		return ErrWorker
	}
	if err != nil || control.ReportPopulation(populationBytes) != nil {
		return ErrWorker
	}
	if err := control.AwaitProvider(); err != nil {
		return err
	}

	verified, err := published.Reinspect(ctx)
	if err != nil {
		return err
	}
	verifiedOpen := true
	defer func() {
		if verifiedOpen {
			resultErr = errors.Join(resultErr, verified.Close())
		}
	}()
	if err := authority.verify(ctx); err != nil {
		return err
	}
	cwd, err := verified.DuplicateDirectory(ctx)
	if err != nil {
		return err
	}
	if err := verified.Close(); err != nil {
		verifiedOpen = false
		_ = cwd.Close()
		return err
	}
	verifiedOpen = false
	if err := authority.verify(ctx); err != nil {
		_ = cwd.Close()
		return err
	}

	home := filepath.Join(config.RuntimePath, HomeName)
	temp := filepath.Join(config.RuntimePath, TempName)
	token := filepath.Join(config.RuntimePath, AttemptTokenName)
	environment := []string{
		"DARK_FACTORY_SOCKET=" + config.AttemptSocket,
		"DARK_FACTORY_ATTEMPT_TOKEN_FILE=" + token,
		"HOME=" + home, "TMPDIR=" + temp, "PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C", "TERM=dumb", "NO_COLOR=1",
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(published.Path()), "GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false", "GIT_SSH_COMMAND=/usr/bin/false", "GH_CONFIG_DIR=/dev/null",
	}
	initialInput := bytes.Clone(config.InitialTerminalInput)
	if len(initialInput) != 0 && initialInput[len(initialInput)-1] != '\n' {
		initialInput = append(initialInput, '\n')
	}
	spec, err := runner.PrepareExecSpec(runner.ExecSpec{Target: shellPath, Args: []string{"-s"}, Env: environment, Cwd: published.Path()})
	if err != nil {
		_ = cwd.Close()
		return err
	}
	if err := authority.verify(ctx); err != nil {
		_ = cwd.Close()
		return err
	}
	if err := authority.close(); err != nil {
		_ = cwd.Close()
		return err
	}
	if err := control.RegisterProviderInput(initialInput); err != nil {
		_ = cwd.Close()
		return err
	}
	return control.ExecProvider(spec, cwd)
}

func openRuntimeAuthority(ctx context.Context, runtimeDir *os.File) (*runtimeAuthority, Config, error) {
	if runtimeDir == nil || ctx.Err() != nil {
		return nil, Config{}, ErrWorker
	}
	rootID, err := privateDirectory(runtimeDir)
	if err != nil {
		return nil, Config{}, err
	}
	config, configID, configSize, encoded, err := openPrivateFile(int(runtimeDir.Fd()), ConfigName, ConfigLimit, rootID.Device)
	if err != nil {
		return nil, Config{}, err
	}
	decoded, err := DecodeConfig(encoded)
	if err != nil {
		_ = config.Close()
		return nil, Config{}, err
	}
	if decoded.RuntimeIdentity != rootID {
		_ = config.Close()
		return nil, Config{}, ErrWorker
	}
	namedRoot, err := openCanonicalDirectory(decoded.RuntimePath)
	if err != nil {
		_ = config.Close()
		return nil, Config{}, err
	}
	namedID, err := privateDirectory(namedRoot)
	if err != nil || namedID != rootID {
		_ = namedRoot.Close()
		_ = config.Close()
		return nil, Config{}, ErrWorker
	}
	home, homeID, err := openPrivateDirectoryAt(int(runtimeDir.Fd()), HomeName, rootID.Device)
	if err != nil {
		_ = namedRoot.Close()
		_ = config.Close()
		return nil, Config{}, err
	}
	temp, tempID, err := openPrivateDirectoryAt(int(runtimeDir.Fd()), TempName, rootID.Device)
	if err != nil {
		_ = home.Close()
		_ = namedRoot.Close()
		_ = config.Close()
		return nil, Config{}, err
	}
	token, tokenID, _, body, err := openPrivateFile(int(runtimeDir.Fd()), AttemptTokenName, 32, rootID.Device)
	if err != nil || len(body) != 32 {
		if token != nil {
			_ = token.Close()
		}
		_ = temp.Close()
		_ = home.Close()
		_ = namedRoot.Close()
		_ = config.Close()
		return nil, Config{}, ErrWorker
	}
	return &runtimeAuthority{runtimePath: decoded.RuntimePath, namedRoot: namedRoot, config: config, home: home, temp: temp, token: token, rootID: rootID, configID: configID, homeID: homeID, tempID: tempID, tokenID: tokenID, configSize: configSize, configDigest: sha256.Sum256(encoded)}, decoded, nil
}

func (a *runtimeAuthority) verify(ctx context.Context) error {
	if a == nil || ctx.Err() != nil {
		if ctx != nil {
			return ctx.Err()
		}
		return ErrWorker
	}
	rootID, err := privateDirectory(a.root)
	if err != nil || rootID != a.rootID {
		return ErrWorker
	}
	namedID, err := privateDirectory(a.namedRoot)
	if err != nil || namedID != a.rootID {
		return ErrWorker
	}
	reopened, err := openCanonicalDirectory(a.runtimePath)
	if err != nil {
		return ErrWorker
	}
	reopenedID, reopenedErr := privateDirectory(reopened)
	closeErr := reopened.Close()
	if reopenedErr != nil || closeErr != nil || reopenedID != a.rootID {
		return ErrWorker
	}
	for _, item := range []struct {
		file *os.File
		id   runner.FileIdentity
	}{{a.home, a.homeID}, {a.temp, a.tempID}} {
		got, err := privateDirectory(item.file)
		if err != nil || got != item.id || runtimeDevice(got, a.rootID.Device) != nil {
			return ErrWorker
		}
	}
	if err := verifyOpenPrivateFile(a.config, a.configID, a.configSize); err != nil || runtimeDevice(a.configID, a.rootID.Device) != nil {
		return ErrWorker
	}
	var token unix.Stat_t
	if err := unix.Fstat(int(a.token.Fd()), &token); err != nil || token.Mode&unix.S_IFMT != unix.S_IFREG || token.Uid != uint32(os.Geteuid()) || token.Mode&0o7777 != 0o600 || token.Nlink != 1 || token.Size != 32 {
		return ErrWorker
	}
	if (runner.FileIdentity{Device: uint64(token.Dev), Inode: token.Ino}) != a.tokenID || runtimeDevice(a.tokenID, a.rootID.Device) != nil {
		return ErrWorker
	}
	if _, err := a.config.Seek(0, io.SeekStart); err != nil {
		return ErrWorker
	}
	body, err := io.ReadAll(io.LimitReader(a.config, int64(ConfigLimit)+1))
	if err != nil || sha256.Sum256(body) != a.configDigest {
		return ErrWorker
	}
	for _, item := range []struct {
		name string
		id   runner.FileIdentity
	}{{ConfigName, a.configID}, {HomeName, a.homeID}, {TempName, a.tempID}, {AttemptTokenName, a.tokenID}} {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(a.root.Fd()), item.name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil || (runner.FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}) != item.id {
			return ErrWorker
		}
	}
	return ctx.Err()
}

func (a *runtimeAuthority) close() error {
	if a == nil {
		return nil
	}
	var err error
	for _, file := range []*os.File{a.token, a.temp, a.home, a.config, a.namedRoot, a.root} {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
	}
	a.root, a.namedRoot, a.config, a.home, a.temp, a.token = nil, nil, nil, nil, nil, nil
	return err
}

func privateDirectory(file *os.File) (runner.FileIdentity, error) {
	if file == nil {
		return runner.FileIdentity{}, ErrWorker
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o700 || stat.Dev == 0 || stat.Ino == 0 {
		return runner.FileIdentity{}, ErrWorker
	}
	return runner.FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func openPrivateDirectoryAt(parent int, name string, rootDevice uint64) (*os.File, runner.FileIdentity, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, runner.FileIdentity{}, ErrWorker
	}
	file := os.NewFile(uintptr(fd), "private-runtime-directory")
	identity, err := privateDirectory(file)
	if err != nil || runtimeDevice(identity, rootDevice) != nil {
		_ = file.Close()
		return nil, runner.FileIdentity{}, ErrWorker
	}
	return file, identity, nil
}

func openPrivateFile(parent int, name string, maximum int, rootDevice uint64) (*os.File, runner.FileIdentity, int64, []byte, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, runner.FileIdentity{}, 0, nil, ErrWorker
	}
	file := os.NewFile(uintptr(fd), "private-runtime-file")
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > int64(maximum) {
		_ = file.Close()
		return nil, runner.FileIdentity{}, 0, nil, ErrWorker
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || int64(len(body)) != stat.Size {
		_ = file.Close()
		return nil, runner.FileIdentity{}, 0, nil, ErrWorker
	}
	id := runner.FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}
	if err := verifyOpenPrivateFile(file, id, stat.Size); err != nil || runtimeDevice(id, rootDevice) != nil {
		_ = file.Close()
		return nil, runner.FileIdentity{}, 0, nil, ErrWorker
	}
	return file, id, stat.Size, body, nil
}

func runtimeDevice(identity runner.FileIdentity, rootDevice uint64) error {
	if rootDevice == 0 || identity.Device != rootDevice {
		return ErrWorker
	}
	return nil
}

func verifyOpenPrivateFile(file *os.File, id runner.FileIdentity, size int64) error {
	var stat unix.Stat_t
	if file == nil || unix.Fstat(int(file.Fd()), &stat) != nil || uint64(stat.Dev) != id.Device || stat.Ino != id.Inode || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 || stat.Size != size {
		return ErrWorker
	}
	return nil
}

func openCanonicalDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || !strings.HasPrefix(path, "/") {
		return nil, ErrWorker
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrWorker
	}
	current := os.NewFile(uintptr(fd), "runtime-path-root")
	for _, component := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		nextFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		_ = current.Close()
		if openErr != nil {
			return nil, ErrWorker
		}
		current = os.NewFile(uintptr(nextFD), "runtime-path-component")
	}
	return current, nil
}
