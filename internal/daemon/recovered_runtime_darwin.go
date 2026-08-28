//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

const recoveredTerminalLimit = 32 << 10

// RecoveredRuntime is a read-only capability for one exact populated runtime
// whose live owner no longer holds the lifetime lease. It deliberately exposes
// none of Runtime's publish, bind, activation, or runner descriptor methods.
type RecoveredRuntime struct {
	runtime *Runtime
	files   map[string]unix.Stat_t
}

type RecoveredRuntimeEvidence struct {
	AttemptToken bool
	WorkerConfig bool
	Terminal     *runner.TerminalRecord
}

func (RecoveredRuntimeEvidence) String() string   { return "recovered runtime evidence (private)" }
func (RecoveredRuntimeEvidence) GoString() string { return "daemon.RecoveredRuntimeEvidence{private}" }

// OpenRecoveredRuntime opens existing evidence without creating, repairing,
// deleting, or following any runtime entry. The exact Store root identity and
// run-id basename are retained and rechecked by every later operation.
func OpenRecoveredRuntime(ctx context.Context, parent *RuntimeParent, basename string, expected runner.FileIdentity) (*RecoveredRuntime, error) {
	return openRecoveredRuntime(ctx, parent, basename, expected, nil)
}

func openRecoveredRuntime(ctx context.Context, parent *RuntimeParent, basename string, expected runner.FileIdentity, afterOpen func()) (result *RecoveredRuntime, resultErr error) {
	if ctx == nil || parent == nil || !validRuntimeName(basename) || expected.Device == 0 || expected.Inode == 0 {
		return nil, invalidContract(nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, invalidContract(err)
	}
	operation, err := parent.begin()
	if err != nil {
		return nil, err
	}
	var child *runtimeParentChild
	defer func() {
		if child == nil {
			resultErr = errors.Join(resultErr, operation.Close())
		} else {
			resultErr = errors.Join(resultErr, child.Close())
		}
	}()
	ownedParent, err := operation.directory()
	if err != nil {
		return nil, err
	}
	parentFD := int(ownedParent.Fd())
	named, err := inspectNamedPrivateDirectory(parentFD, basename)
	if err != nil || named.fileIdentity() != expected {
		return nil, invalidContract(err)
	}
	fd, err := unix.Openat(parentFD, basename, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	dir := os.NewFile(uintptr(fd), "recovered-attempt-runtime")
	keepDir := false
	defer func() {
		if !keepDir {
			resultErr = errors.Join(resultErr, dir.Close())
		}
	}()
	if afterOpen != nil {
		afterOpen()
	}
	if err := ctx.Err(); err != nil {
		return nil, invalidContract(err)
	}
	opened, err := inspectExpectedDirectory(fd, expected)
	if err != nil || opened != named || verifyNamedChild(parentFD, basename, opened) != nil {
		return nil, invalidContract(err)
	}
	lifetime, lifetimeID, err := openRuntimeLifetime(fd, opened.device, nil)
	if err != nil {
		return nil, err
	}
	keepLifetime := false
	defer func() {
		if !keepLifetime {
			resultErr = errors.Join(resultErr, lifetime.Close())
		}
	}()
	home, temp, err := inspectRuntimeLayout(fd, opened)
	if err != nil {
		return nil, invalidContract(err)
	}
	files, err := inspectRecoveredRuntimeCensus(fd, opened.device)
	if err != nil {
		return nil, err
	}
	locator, err := operation.locator(basename)
	if err != nil {
		return nil, err
	}
	child, err = operation.transfer()
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		locator: locator, basename: basename, dir: dir, directory: opened,
		owner: child, identity: expected, home: home, temp: temp,
		lifetime: lifetime, lifetimeID: lifetimeID,
	}
	recovered := &RecoveredRuntime{runtime: runtime, files: files}
	if err := recovered.verifyAuthority(); err != nil {
		return nil, err
	}
	keepDir = true
	keepLifetime = true
	child = nil
	return recovered, nil
}

// InspectEvidence validates retained token/config bytes against durable
// expectations without returning token bytes or interpreting configured paths.
// expectedConfig must be supplied whenever a config file is present.
func (recovered *RecoveredRuntime) InspectEvidence(ctx context.Context, credential kernel.AttemptDigest, expectedConfig *changeworker.Config, requireConfig bool) (RecoveredRuntimeEvidence, error) {
	if recovered == nil || recovered.runtime == nil || ctx == nil {
		return RecoveredRuntimeEvidence{}, invalidContract(nil)
	}
	recovered.runtime.mu.Lock()
	defer recovered.runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return RecoveredRuntimeEvidence{}, invalidContract(err)
	}
	if err := recovered.verifyAuthority(); err != nil {
		return RecoveredRuntimeEvidence{}, err
	}
	result := RecoveredRuntimeEvidence{}
	if _, present := recovered.files[attemptTokenName]; present {
		body, err := recovered.readFile(ctx, attemptTokenName, 32)
		if err != nil {
			return RecoveredRuntimeEvidence{}, err
		}
		digest := sha256.Sum256(body)
		if !bytes.Equal(digest[:], credential.Bytes()) {
			return RecoveredRuntimeEvidence{}, invalidContract(nil)
		}
		result.AttemptToken = true
	}
	if _, present := recovered.files[workerConfigName]; present {
		if !result.AttemptToken || expectedConfig == nil {
			return RecoveredRuntimeEvidence{}, invalidContract(nil)
		}
		body, err := recovered.readFile(ctx, workerConfigName, workerConfigLimit)
		if err != nil {
			return RecoveredRuntimeEvidence{}, err
		}
		decoded, err := changeworker.DecodeConfig(body)
		if err != nil || decoded.RuntimePath != recovered.runtime.locator || decoded.RuntimeIdentity != recovered.runtime.identity {
			return RecoveredRuntimeEvidence{}, invalidContract(err)
		}
		expected, err := changeworker.EncodeConfig(*expectedConfig)
		if err != nil || !bytes.Equal(body, expected) {
			return RecoveredRuntimeEvidence{}, invalidContract(err)
		}
		result.WorkerConfig = true
	}
	if requireConfig && (!result.AttemptToken || !result.WorkerConfig) {
		return RecoveredRuntimeEvidence{}, invalidContract(nil)
	}
	if expected, present := recovered.files[runner.TerminalSpoolName]; present {
		record, err := runner.LoadTerminal(recovered.runtime.dir, runner.TerminalSpoolName)
		if err != nil || record.Identity != fileIdentity(expected) {
			return RecoveredRuntimeEvidence{}, invalidContract(err)
		}
		result.Terminal = record
	}
	if err := recovered.verifyAuthority(); err != nil {
		return RecoveredRuntimeEvidence{}, err
	}
	return result, nil
}

// AcknowledgeTerminal removes exactly the inspected spool only after the
// caller supplies a durable Store postcondition for the same run and exact
// released provider process/group. Exit time is deliberately not compared:
// a restart may observe the same committed exit with a new proposed time.
func (recovered *RecoveredRuntime) AcknowledgeTerminal(record *runner.TerminalRecord, run kernel.Run, runtimeRoot, providerProcess, providerGroup kernel.Resource) error {
	if recovered == nil || recovered.runtime == nil || record == nil || !terminalCommitProven(recovered.runtime.locator, recovered.runtime.identity, record.Terminal, run, runtimeRoot, providerProcess, providerGroup) {
		return invalidContract(nil)
	}
	recovered.runtime.mu.Lock()
	defer recovered.runtime.mu.Unlock()
	if err := recovered.verifyAuthority(); err != nil {
		return err
	}
	expected, present := recovered.files[runner.TerminalSpoolName]
	if !present || record.Identity != fileIdentity(expected) {
		return invalidContract(nil)
	}
	if err := runner.AcknowledgeTerminal(recovered.runtime.dir, runner.TerminalSpoolName, record, true); err != nil {
		return invalidContract(err)
	}
	delete(recovered.files, runner.TerminalSpoolName)
	return recovered.verifyAuthority()
}

func terminalCommitProven(runtimePath string, runtimeIdentity runner.FileIdentity, terminal runner.Terminal, run kernel.Run, runtimeRoot, providerProcess, providerGroup kernel.Resource) bool {
	basename := run.ID.String()
	if filepath.Base(runtimePath) != basename || run.Phase != kernel.RunFinalizing || run.ProviderExit == nil || terminal.AttemptID != basename || run.CredentialRevokedAt == nil || run.FinalizingAt == nil {
		return false
	}
	wantRuntimeIdentity, err := pathResourceIdentity(runtimeIdentity)
	if err != nil || runtimeRoot.RunID != run.ID || runtimeRoot.Kind != kernel.ResourceRuntimeRoot || runtimeRoot.Path != runtimePath || runtimeRoot.Identity != wantRuntimeIdentity || runtimeRoot.Identity.Empty() || runtimeRoot.ActivatedAt == nil || (runtimeRoot.State != kernel.ResourceReleasing && runtimeRoot.State != kernel.ResourceUnresolved) {
		return false
	}
	for _, resource := range []kernel.Resource{providerProcess, providerGroup} {
		if resource.RunID != run.ID || resource.State != kernel.ResourceReleased || resource.ActivatedAt == nil || resource.ReleasedAt == nil || resource.Identity.Empty() {
			return false
		}
	}
	if providerProcess.Kind != kernel.ResourceProviderProcess || providerGroup.Kind != kernel.ResourceProviderGroup || providerProcess.Identity != providerGroup.Identity || *providerProcess.ActivatedAt != *providerGroup.ActivatedAt || run.ProviderExit.At().Int64() < providerProcess.ActivatedAt.Int64() || providerProcess.ReleasedAt.Int64() < run.ProviderExit.At().Int64() || providerGroup.ReleasedAt.Int64() < run.ProviderExit.At().Int64() {
		return false
	}
	identity, err := runnerIdentity(providerProcess.Identity)
	if err != nil || terminal.Process != identity || run.ProviderExit.Sequence() != 1 || run.ProviderExit.RecoveredAbsence() {
		return false
	}
	if code, present := run.ProviderExit.Code(); present {
		return terminal.Exit.Code >= 0 && terminal.Exit.Signal == 0 && code == int64(terminal.Exit.Code)
	}
	signal, present := run.ProviderExit.Signal()
	return present && terminal.Exit.Code == -1 && terminal.Exit.Signal > 0 && signal == int64(terminal.Exit.Signal)
}

func (recovered *RecoveredRuntime) verifyAuthority() error {
	if recovered == nil || recovered.runtime == nil {
		return invalidContract(nil)
	}
	if err := recovered.runtime.verifyAuthority(); err != nil {
		return invalidContract(err)
	}
	actual, err := inspectRecoveredRuntimeCensus(int(recovered.runtime.dir.Fd()), recovered.runtime.directory.device)
	if err != nil || !sameRecoveredCensus(actual, recovered.files) {
		return invalidContract(err)
	}
	return nil
}

func inspectRecoveredRuntimeCensus(rootFD int, device uint64) (map[string]unix.Stat_t, error) {
	entries, more, err := readRuntimeEntries(rootFD, runtimeTopEntryLimit)
	if err != nil || more {
		return nil, invalidContract(err)
	}
	home, temp, lifetime := false, false, false
	files := make(map[string]unix.Stat_t, runtimeTopEntryLimit)
	for _, name := range entries {
		switch name {
		case runtimeHomeName:
			home = true
			continue
		case runtimeTempName:
			temp = true
			continue
		case runner.RuntimeLifetimeLeaseName:
			lifetime = true
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(rootFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil || !validRecoveredRuntimeFile(name, stat, device) {
			return nil, invalidContract(err)
		}
		files[name] = stat
	}
	if !home || !temp || !lifetime {
		return nil, invalidContract(nil)
	}
	token := hasRecoveredFile(files, attemptTokenName)
	config := hasRecoveredFile(files, workerConfigName)
	outer := hasRecoveredFile(files, runner.OuterActivationMarkerName)
	inner := hasRecoveredFile(files, runner.InnerActivationMarkerName)
	terminal := hasRecoveredFile(files, runner.TerminalSpoolName)
	terminalScratch := hasRecoveredFile(files, runner.TerminalScratchName)
	gateConfig := hasRecoveredFile(files, runner.GateConfigScratchName)
	gateStdin := hasRecoveredFile(files, runner.GateStdinScratchName)
	// These are the fixed crash cuts emitted by the producer. The outer gate
	// precedes the inner gate, but the terminal may outlive the inner marker
	// after pre-activation cleanup. Each named scratch is created and removed
	// synchronously, so each gate scratch is an outer/inner prefix by itself.
	gateScratch := gateConfig || gateStdin
	terminalResidue := terminalScratch || terminal
	residue := outer || inner || gateScratch || terminalResidue
	// The provider consumes change-worker.config after the inner gate and before
	// exec. A crash after that irreversible cut can therefore leave exact
	// activation or terminal evidence without the config pathname. Token remains
	// mandatory, while a gate scratch still proves the cut was pre-consumption.
	if config && !token || residue && !token || gateScratch && !config {
		return nil, invalidContract(nil)
	}
	if inner && !outer || terminalResidue && !outer {
		return nil, invalidContract(nil)
	}
	if terminalScratch && terminal {
		return nil, invalidContract(nil)
	}
	if gateConfig && gateStdin {
		return nil, invalidContract(nil)
	}
	if gateScratch && inner {
		return nil, invalidContract(nil)
	}
	if gateScratch && terminalResidue {
		return nil, invalidContract(nil)
	}
	if gateStdin && outer {
		return nil, invalidContract(nil)
	}
	return files, nil
}

func hasRecoveredFile(files map[string]unix.Stat_t, name string) bool {
	_, present := files[name]
	return present
}

func validRecoveredRuntimeFile(name string, stat unix.Stat_t, device uint64) bool {
	if stat.Dev == 0 || stat.Ino == 0 || !validRuntimeOrdinaryFile(stat, device, true) {
		return false
	}
	switch name {
	case attemptTokenName:
		return stat.Size == 32
	case workerConfigName:
		return stat.Size > 0 && stat.Size <= workerConfigLimit
	case runner.OuterActivationMarkerName, runner.InnerActivationMarkerName,
		runner.GateConfigScratchName, runner.GateStdinScratchName:
		return stat.Size == 0
	case runner.TerminalSpoolName:
		return stat.Size > 0 && stat.Size <= recoveredTerminalLimit
	case runner.TerminalScratchName:
		return stat.Size >= 0 && stat.Size <= recoveredTerminalLimit
	default:
		return false
	}
}

func sameRecoveredCensus(left, right map[string]unix.Stat_t) bool {
	if len(left) != len(right) {
		return false
	}
	for name, want := range right {
		got, found := left[name]
		if !found || !sameRecoveredFileStat(got, want) {
			return false
		}
	}
	return true
}

func sameRecoveredFileStat(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Uid == right.Uid && left.Gid == right.Gid && left.Mode == right.Mode && left.Nlink == right.Nlink && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func fileIdentity(stat unix.Stat_t) runner.FileIdentity {
	return runner.FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}
}

func (recovered *RecoveredRuntime) readFile(ctx context.Context, name string, limit int) (_ []byte, resultErr error) {
	want, present := recovered.files[name]
	if !present || limit < 1 || want.Size < 1 || want.Size > int64(limit) {
		return nil, invalidContract(nil)
	}
	fd, err := unix.Openat(int(recovered.runtime.dir.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	file := os.NewFile(uintptr(fd), "recovered-runtime-evidence")
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	var before, after, named unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || !sameRecoveredFileStat(before, want) {
		return nil, invalidContract(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, invalidContract(err)
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(body) != int(want.Size) || len(body) > limit {
		return nil, invalidContract(err)
	}
	if err := unix.Fstat(fd, &after); err != nil || !sameRecoveredFileStat(before, after) {
		return nil, invalidContract(err)
	}
	if err := unix.Fstatat(int(recovered.runtime.dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameRecoveredFileStat(after, named) {
		return nil, invalidContract(err)
	}
	return body, nil
}

func (recovered *RecoveredRuntime) Close() error {
	if recovered == nil || recovered.runtime == nil {
		return nil
	}
	return recovered.runtime.Close()
}
