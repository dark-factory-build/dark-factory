//go:build darwin

package changeworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/provider"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

type runtimeAuthority struct {
	root, namedRoot, home, temp, token *os.File
	runtimePath                        string
	rootID                             runner.FileIdentity
	homeID, tempID, tokenID            runner.FileIdentity
}

const providerTaskName = ".provider-task"

func runProvider(ctx context.Context) (resultErr error) {
	control, err := runner.OpenWorkerControl()
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			// The worker may fail during the final source/authority scan or task
			// sealing. Preserve that exact bounded cause for the outer owner
			// instead of reducing it to an unexplained EOF.
			_ = control.ReportProviderError(resultErr)
		}
		resultErr = errors.Join(resultErr, control.Close())
	}()
	encodedConfig, err := control.ReadConfig(ConfigLimit)
	if err != nil {
		return err
	}
	config, err := DecodeConfig(encodedConfig)
	if err != nil {
		return err
	}
	runtimeDir, err := control.DuplicateRuntimeDirectory(ctx)
	if err != nil {
		return err
	}
	authority, err := openRuntimeAuthority(ctx, runtimeDir, config)
	if err != nil {
		_ = runtimeDir.Close()
		return err
	}
	authority.root = runtimeDir
	defer func() { resultErr = errors.Join(resultErr, authority.close()) }()
	factoryctl, err := runner.CommitExecutableLocator(config.FactoryctlExecutable)
	if err != nil {
		return err
	}

	home := filepath.Join(config.RuntimePath, HomeName)
	var cwd *os.File
	gitDirectory := config.GitCommonDir
	publishedPath := filepath.Join(config.ChangeParent, config.FinalName)
	if config.Role == kernel.RoleOrchestrator {
		// An orchestrator has no Change. It walks the same checkpoints with
		// nothing to report, so the daemon's stage grammar is unchanged, and
		// works in its private runtime home.
		if control.ReportSelection(nil) != nil || control.AwaitPreparation() != nil || control.ReportPreparation(nil) != nil || control.AwaitPopulation() != nil {
			return ErrWorker
		}
		if err := reportPopulation(control); err != nil {
			return err
		}
		if err := control.AwaitProvider(); err != nil {
			return err
		}
		publishedPath = home
		cwd, err = authority.openHome(ctx, runtimeDir)
	} else {
		cwd, gitDirectory, err = openChangeDirectory(ctx, control, config, authority)
	}
	if err != nil {
		return err
	}

	temp := filepath.Join(config.RuntimePath, TempName)
	token := filepath.Join(config.RuntimePath, AttemptTokenName)
	runtimePaths, err := provider.NewRuntimePaths(
		home, temp, config.AttemptSocket, token, factoryctl.Path(), filepath.Dir(publishedPath), config.ToolPath, config.AccountHome, config.AccountConfigDir, config.ToolchainReadRoots,
	)
	if err != nil {
		_ = cwd.Close()
		return err
	}
	if config.Role == kernel.RoleWorker && config.LocalCILeaseDir == "" {
		fmt.Fprintln(os.Stderr, "factory: shared local CI lease unavailable; continue source work, but required CI needs host preparation before it can run")
	}
	runtimePaths, err = runtimePaths.WithLocalCILeaseDirectory(config.LocalCILeaseDir)
	if err != nil {
		_ = cwd.Close()
		return err
	}
	// Grant the verified worktree's actual administration. New Changes have
	// private Git state; retained canonical worktrees keep their existing layout.
	runtimePaths, err = runtimePaths.WithGitCommonDirectory(gitDirectory, config.Role == kernel.RoleWorker)
	if err != nil {
		_ = cwd.Close()
		return err
	}
	installation, err := provider.ResolveInstallation(config.Provider, config.ToolPath)
	if err != nil {
		_ = cwd.Close()
		return err
	}
	// Keep the one verified publication path as the authority for both Codex's
	// project policy and the runner's process cwd below.
	request, err := provider.NewRequest(config.Provider, installation, config.Model, config.ReasoningEffort, runtimePaths, publishedPath, config.Role, config.AgentID, config.TaskIncarnationID)
	if err != nil {
		_ = cwd.Close()
		return err
	}
	if config.PreviousWorkingDirectory != "" {
		request, err = request.WithPreviousWorkingDirectory(config.PreviousWorkingDirectory)
		if err != nil {
			_ = cwd.Close()
			return err
		}
	}
	launch, err := provider.Build(request)
	if err != nil {
		_ = cwd.Close()
		return err
	}
	delivery, program, err := prepareProviderTask(config.Provider, config.ProviderTask)
	if err != nil {
		_ = cwd.Close()
		return err
	}
	if delivery != launch.TaskDelivery() {
		_ = cwd.Close()
		return provider.ErrInvalid
	}
	spec, err := runner.PrepareCommittedExecSpec(launch.Executable(), launch.Argv(), launch.Environment(), publishedPath)
	if err != nil {
		_ = cwd.Close()
		return err
	}
	if err := authority.verify(ctx); err != nil {
		_ = cwd.Close()
		return fmt.Errorf("runtime authority verification: %w", err)
	}
	if err := factoryctl.Verify(); err != nil {
		_ = cwd.Close()
		return err
	}
	if err := launch.Executable().Verify(); err != nil {
		_ = cwd.Close()
		return err
	}
	var task *os.File
	if delivery == provider.TaskDeliveryFD11 {
		task, err = authority.sealProviderTask(config.Provider, program)
		if err != nil {
			_ = cwd.Close()
			return err
		}
	} else if delivery != provider.TaskDeliveryStartupTerminal && delivery != provider.TaskDeliveryAttemptAPI {
		_ = cwd.Close()
		return provider.ErrInvalid
	}
	clear(program)
	taskOpen := task != nil
	defer func() {
		if task != nil && taskOpen {
			resultErr = errors.Join(resultErr, task.Close())
		}
	}()
	// Retain the descriptor-bound runtime authority until exec. Its members are
	// CLOEXEC, so a successful provider image receives none of them; a failed
	// exec returns through the ordinary defer and closes them. Shell's task is
	// unlinked and read-only; the attempt runner separately owns Claude startup,
	// while Codex receives no task-bearing descriptor.
	if err := authority.verify(ctx); err != nil {
		_ = cwd.Close()
		return fmt.Errorf("runtime authority verification: %w", err)
	}
	// The one effect outside the runtime, so it is the last thing this
	// process does before handing over to exec. The runner's own pre-exec
	// checks can still refuse; a record left for a Change that never ran
	// names a directory only the daemon makes, and costs nothing else.
	if config.Provider == kernel.ProviderClaudeCode {
		if err := provider.TrustClaudeDirectory(runtimePaths, publishedPath); err != nil {
			_ = cwd.Close()
			return err
		}
	}
	taskOpen = false
	return control.ExecProvider(spec, cwd, task)
}

// openChangeDirectory makes or reopens the run's Change worktree and returns
// its directory and verified Git administration.
func openChangeDirectory(ctx context.Context, control *runner.WorkerControl, config Config, authority *runtimeAuthority) (*os.File, string, error) {
	var expected change.WorktreeFacts
	var err error
	if config.Retained == nil {
		expected, err = prepareFreshChange(ctx, control, config)
	} else {
		expected, err = openRetainedChange(ctx, control, config)
	}
	if err != nil {
		return nil, "", err
	}
	if err := authority.verify(ctx); err != nil {
		return nil, "", fmt.Errorf("runtime authority verification: %w", err)
	}
	path := filepath.Join(config.ChangeParent, config.FinalName)
	// The provider has not run: the branch must still be where it was left.
	facts, err := change.InspectWorktree(ctx, config.GitExecutable, config.RepositoryRoot, config.RepositoryIdentity, path)
	if err != nil {
		return nil, "", err
	}
	if facts.Branch() != expected.Branch() || !facts.Head().Equal(expected.Head()) || facts.GitDirectory() != expected.GitDirectory() {
		return nil, "", errors.Join(ErrWorker, errors.New("Change worktree moved before the provider ran"))
	}
	cwd, err := openWorktreeDirectory(path)
	if err != nil {
		return nil, "", err
	}
	if err := authority.verify(ctx); err != nil {
		_ = cwd.Close()
		return nil, "", fmt.Errorf("runtime authority verification: %w", err)
	}
	return cwd, facts.GitDirectory(), nil
}

// openWorktreeDirectory opens the worktree root as the provider's working
// directory: an owned private directory holding the gitfile that makes it
// a linked worktree.
func openWorktreeDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, ErrWorker
	}
	directory := os.NewFile(uintptr(fd), "change-worktree")
	if _, err := privateDirectory(directory); err != nil {
		_ = directory.Close()
		return nil, err
	}
	var gitFile unix.Stat_t
	if err := unix.Fstatat(fd, ".git", &gitFile, unix.AT_SYMLINK_NOFOLLOW); err != nil || gitFile.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = directory.Close()
		return nil, ErrWorker
	}
	return directory, nil
}

// openHome returns a fresh descriptor for the private runtime home the
// authority already holds, checked to be that same directory.
func (a *runtimeAuthority) openHome(ctx context.Context, runtimeDir *os.File) (*os.File, error) {
	if err := a.verify(ctx); err != nil {
		return nil, fmt.Errorf("runtime authority verification: %w", err)
	}
	home, homeID, err := openPrivateDirectoryAt(int(runtimeDir.Fd()), HomeName, a.rootID.Device)
	if err != nil {
		return nil, err
	}
	if homeID != a.homeID {
		_ = home.Close()
		return nil, ErrWorker
	}
	return home, nil
}

// sealProviderTask turns the admission-owned bytes into one exact, unlinked,
// read-only descriptor. The temporary name exists only inside the already
// registered private runtime and is removed before the descriptor crosses the
// runner boundary. No pathname is carried into provider exec.
func (a *runtimeAuthority) sealProviderTask(kind kernel.Provider, task []byte) (_ *os.File, resultErr error) {
	if _, _, err := provider.PrepareTask(kind, task); a == nil || a.temp == nil || err != nil {
		return nil, ErrWorker
	}
	tempID, err := privateDirectory(a.temp)
	if err != nil || tempID != a.tempID {
		return nil, ErrWorker
	}
	fd, err := unix.Openat(int(a.temp.Fd()), providerTaskName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0o600)
	if err != nil {
		return nil, ErrWorker
	}
	writer := os.NewFile(uintptr(fd), "provider-task-writer")
	var created runner.FileIdentity
	named := false
	var reader *os.File
	defer func() {
		if writer != nil {
			resultErr = errors.Join(resultErr, writer.Close())
		}
		if resultErr != nil && reader != nil {
			resultErr = errors.Join(resultErr, reader.Close())
		}
		if named && created.Device != 0 && created.Inode != 0 {
			var current unix.Stat_t
			if unix.Fstatat(int(a.temp.Fd()), providerTaskName, &current, unix.AT_SYMLINK_NOFOLLOW) == nil && uint64(current.Dev) == created.Device && current.Ino == created.Inode {
				resultErr = errors.Join(resultErr, unix.Unlinkat(int(a.temp.Fd()), providerTaskName, 0))
				resultErr = errors.Join(resultErr, unix.Fsync(int(a.temp.Fd())))
			}
		}
	}()
	var empty unix.Stat_t
	if err := unix.Fstat(fd, &empty); err != nil || empty.Mode&unix.S_IFMT != unix.S_IFREG || empty.Uid != uint32(os.Geteuid()) || empty.Mode&0o7777 != 0o600 || empty.Nlink != 1 || empty.Size != 0 || empty.Dev == 0 || empty.Ino == 0 || uint64(empty.Dev) != a.tempID.Device {
		return nil, ErrWorker
	}
	created = runner.FileIdentity{Device: uint64(empty.Dev), Inode: empty.Ino}
	named = true
	n, err := writer.Write(task)
	if err != nil || n != len(task) || writer.Sync() != nil {
		return nil, ErrWorker
	}
	readFD, err := unix.Openat(int(a.temp.Fd()), providerTaskName, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, ErrWorker
	}
	reader = os.NewFile(uintptr(readFD), "provider-task")
	var sealed unix.Stat_t
	if err := unix.Fstat(readFD, &sealed); err != nil || sealed.Mode&unix.S_IFMT != unix.S_IFREG || sealed.Uid != uint32(os.Geteuid()) || sealed.Mode&0o7777 != 0o600 || sealed.Nlink != 1 || sealed.Size != int64(len(task)) || uint64(sealed.Dev) != created.Device || sealed.Ino != created.Inode {
		return nil, ErrWorker
	}
	flags, err := unix.FcntlInt(reader.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return nil, ErrWorker
	}
	body, err := io.ReadAll(io.LimitReader(reader, int64(runner.MaxProviderTaskBytes)+1))
	if err != nil || !bytes.Equal(body, task) {
		return nil, ErrWorker
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, ErrWorker
	}
	var namedStat unix.Stat_t
	if err := unix.Fstatat(int(a.temp.Fd()), providerTaskName, &namedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || uint64(namedStat.Dev) != created.Device || namedStat.Ino != created.Inode {
		return nil, ErrWorker
	}
	if err := unix.Unlinkat(int(a.temp.Fd()), providerTaskName, 0); err != nil {
		return nil, ErrWorker
	}
	named = false
	if err := unix.Fsync(int(a.temp.Fd())); err != nil {
		return nil, ErrWorker
	}
	if err := writer.Close(); err != nil {
		writer = nil
		return nil, ErrWorker
	}
	writer = nil
	var unlinked unix.Stat_t
	if err := unix.Fstat(readFD, &unlinked); err != nil || unlinked.Nlink != 0 || unlinked.Size != int64(len(task)) || uint64(unlinked.Dev) != created.Device || unlinked.Ino != created.Inode {
		return nil, ErrWorker
	}
	tempID, err = privateDirectory(a.temp)
	if err != nil || tempID != a.tempID {
		return nil, ErrWorker
	}
	return reader, nil
}

func prepareFreshChange(ctx context.Context, control *runner.WorkerControl, config Config) (change.WorktreeFacts, error) {
	selection, err := change.SelectGit(ctx, config.GitExecutable, config.RepositoryRoot, config.Revision, config.RepositoryIdentity)
	if err != nil {
		return change.WorktreeFacts{}, err
	}
	if !selection.RepositoryIdentity().Equal(config.RepositoryIdentity) || control.ReportSelection(nil) != nil {
		return change.WorktreeFacts{}, ErrWorker
	}
	if err := control.AwaitPreparation(); err != nil {
		return change.WorktreeFacts{}, err
	}
	preparationBytes, err := EncodeResult(Result{Format: selection.ObjectFormat(), Base: selection.Base()})
	if err != nil || control.ReportPreparation(preparationBytes) != nil {
		return change.WorktreeFacts{}, ErrWorker
	}
	if err := control.AwaitPopulation(); err != nil {
		return change.WorktreeFacts{}, err
	}
	path := filepath.Join(config.ChangeParent, config.FinalName)
	facts, err := change.AddPrivateWorktree(ctx, selection, path, change.BranchName(config.FinalName))
	if err != nil {
		return change.WorktreeFacts{}, err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return change.WorktreeFacts{}, ErrWorker
	}
	if err := reportPopulation(control); err != nil {
		return change.WorktreeFacts{}, err
	}
	if err := control.AwaitProvider(); err != nil {
		return change.WorktreeFacts{}, err
	}
	return facts, nil
}

// openRetainedChange reopens a settled Change: its worktree must still be
// on its branch at the head the daemon recorded. A Change from before
// managed worktrees is adopted into one first, at its recorded base, with
// every file as the worker left it.
func openRetainedChange(ctx context.Context, control *runner.WorkerControl, config Config) (change.WorktreeFacts, error) {
	retained := config.Retained
	if retained == nil || change.VerifyRepositoryRoot(config.RepositoryRoot, config.RepositoryIdentity) != nil {
		return change.WorktreeFacts{}, ErrWorker
	}
	if control.ReportSelection(nil) != nil {
		return change.WorktreeFacts{}, ErrWorker
	}
	if err := control.AwaitPreparation(); err != nil {
		return change.WorktreeFacts{}, err
	}
	preparationBytes, err := EncodeResult(*retained)
	if err != nil || control.ReportPreparation(preparationBytes) != nil {
		return change.WorktreeFacts{}, ErrWorker
	}
	if err := control.AwaitPopulation(); err != nil {
		return change.WorktreeFacts{}, err
	}
	path := filepath.Join(config.ChangeParent, config.FinalName)
	branch := change.BranchName(config.FinalName)
	var facts change.WorktreeFacts
	if retained.Head == nil {
		facts, err = change.AdoptWorktree(ctx, config.GitExecutable, config.RepositoryRoot, config.RepositoryIdentity, path, branch, retained.Base)
	} else {
		facts, err = change.InspectWorktree(ctx, config.GitExecutable, config.RepositoryRoot, config.RepositoryIdentity, path)
		if err == nil && (!facts.Head().Equal(*retained.Head) || facts.Branch() != branch) {
			err = errors.New("retained Change worktree is not at its settled head")
		}
		if err == nil {
			// The recorded base must be where the branch's work started.
			var descends bool
			descends, err = change.DescendsFrom(ctx, config.GitExecutable, config.RepositoryRoot, config.RepositoryIdentity, path, retained.Base)
			if err == nil && !descends {
				err = errors.New("retained Change head does not descend from its recorded base")
			}
		}
	}
	if err != nil {
		return change.WorktreeFacts{}, errors.Join(err, ErrWorker)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return change.WorktreeFacts{}, ErrWorker
	}
	if err := reportPopulation(control); err != nil {
		return change.WorktreeFacts{}, err
	}
	if err := control.AwaitProvider(); err != nil {
		return change.WorktreeFacts{}, err
	}
	return facts, nil
}

func reportPopulation(control *runner.WorkerControl) error {
	group := runner.ObserveProcessGroup(control.Identity())
	if group.Presence != runner.Present || len(group.Members) != 1 || group.Members[0] != control.Identity() {
		return ErrWorker
	}
	if control.ReportPopulation(nil) != nil {
		return ErrWorker
	}
	return nil
}

func openRuntimeAuthority(ctx context.Context, runtimeDir *os.File, config Config) (*runtimeAuthority, error) {
	if runtimeDir == nil || ctx.Err() != nil {
		return nil, ErrWorker
	}
	rootID, err := privateDirectory(runtimeDir)
	if err != nil {
		return nil, err
	}
	if config.RuntimeIdentity != rootID {
		return nil, ErrWorker
	}
	namedRoot, err := openCanonicalDirectory(config.RuntimePath)
	if err != nil {
		return nil, err
	}
	namedID, err := privateDirectory(namedRoot)
	if err != nil || namedID != rootID {
		_ = namedRoot.Close()
		return nil, ErrWorker
	}
	home, homeID, err := openPrivateDirectoryAt(int(runtimeDir.Fd()), HomeName, rootID.Device)
	if err != nil {
		_ = namedRoot.Close()
		return nil, err
	}
	temp, tempID, err := openPrivateDirectoryAt(int(runtimeDir.Fd()), TempName, rootID.Device)
	if err != nil {
		_ = home.Close()
		_ = namedRoot.Close()
		return nil, err
	}
	token, tokenID, _, body, err := openPrivateFile(int(runtimeDir.Fd()), AttemptTokenName, 32, rootID.Device)
	if err != nil || len(body) != 32 {
		if token != nil {
			_ = token.Close()
		}
		_ = temp.Close()
		_ = home.Close()
		_ = namedRoot.Close()
		return nil, ErrWorker
	}
	return &runtimeAuthority{runtimePath: config.RuntimePath, namedRoot: namedRoot, home: home, temp: temp, token: token, rootID: rootID, homeID: homeID, tempID: tempID, tokenID: tokenID}, nil
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
	var token unix.Stat_t
	if err := unix.Fstat(int(a.token.Fd()), &token); err != nil || token.Mode&unix.S_IFMT != unix.S_IFREG || token.Uid != uint32(os.Geteuid()) || token.Mode&0o7777 != 0o600 || token.Nlink != 1 || token.Size != 32 {
		return ErrWorker
	}
	if (runner.FileIdentity{Device: uint64(token.Dev), Inode: token.Ino}) != a.tokenID || runtimeDevice(a.tokenID, a.rootID.Device) != nil {
		return ErrWorker
	}
	for _, item := range []struct {
		name string
		id   runner.FileIdentity
	}{{HomeName, a.homeID}, {TempName, a.tempID}, {AttemptTokenName, a.tokenID}} {
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
	for _, file := range []*os.File{a.token, a.temp, a.home, a.namedRoot, a.root} {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
	}
	a.root, a.namedRoot, a.home, a.temp, a.token = nil, nil, nil, nil, nil
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
