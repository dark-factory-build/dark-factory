//go:build darwin

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

const (
	serviceBinaryMaxBytes  = int64(1) << 30
	serviceBootoutPatience = 5 * time.Second
)

var serviceBinaryNames = [3]string{"factoryd", "factoryctl", "factory-runner"}

// withServiceMutation serializes every lifecycle mutation on the exact home
// directory. factoryd's lifetime flock is on the separate factory.lock inode,
// so launchctl may start or stop the daemon while this lock remains held.
func withServiceMutation(ctx context.Context, home string, operation func(*serviceHomeCapability) (ServiceStatus, error)) (status ServiceStatus, resultErr error) {
	if ctx == nil || operation == nil {
		return ServiceStatus{}, fmt.Errorf("%w: invalid service mutation", ErrServiceAmbiguous)
	}
	capability, err := openServiceHomeCapability(ctx, home)
	if err != nil {
		return ServiceStatus{}, err
	}
	if err := unix.Flock(int(capability.home.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeErr := capability.close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return ServiceStatus{}, errors.Join(ErrServiceAmbiguous, ErrBusy, err, closeErr)
		}
		return ServiceStatus{}, errors.Join(ErrServiceAmbiguous, err, closeErr)
	}
	defer func() {
		verifyErr := errors.Join(capability.recheck(ctx), capability.stageAbsent())
		unlockErr := unix.Flock(int(capability.home.Fd()), unix.LOCK_UN)
		closeErr := capability.close()
		if cleanupErr := errors.Join(verifyErr, unlockErr, closeErr); cleanupErr != nil {
			resultErr = errors.Join(resultErr, ErrServiceAmbiguous, cleanupErr)
		}
	}()
	if err := errors.Join(capability.recheck(ctx), capability.stageAbsent()); err != nil {
		return ServiceStatus{}, errors.Join(ErrServiceAmbiguous, err)
	}
	return operation(capability)
}

// openServiceArtifacts opens <home>.service without following symlinks.
// Absence is an ordinary result, never an error.
func openServiceArtifacts(home string) (*os.File, bool, error) {
	if !validServicePath(home) {
		return nil, false, ErrInvalidHome
	}
	path := ServiceDirectoryPath(home)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: open service directory: %v", ErrServiceReceipt, err)
	}
	directory := os.NewFile(uintptr(fd), path)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 {
		_ = directory.Close()
		return nil, false, fmt.Errorf("%w: service directory authority", ErrServiceReceipt)
	}
	return directory, true, nil
}

// readServiceReceipt reads and canonically verifies the durable receipt.
// (zero, false, nil) means provably no receipt.
func readServiceReceipt(home string) (serviceReceipt, bool, error) {
	directory, present, err := openServiceArtifacts(home)
	if err != nil || !present {
		return serviceReceipt{}, false, err
	}
	defer func() { _ = directory.Close() }()
	fd, err := unix.Openat(int(directory.Fd()), serviceReceiptName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return serviceReceipt{}, false, nil
	}
	if err != nil {
		return serviceReceipt{}, false, fmt.Errorf("%w: open", ErrServiceReceipt)
	}
	file := os.NewFile(uintptr(fd), serviceReceiptName)
	defer func() { _ = file.Close() }()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0o7777 != 0o600 || before.Uid != uint32(os.Geteuid()) || before.Nlink != 1 || before.Size <= 0 || before.Size > serviceReceiptMaxBytes {
		return serviceReceipt{}, false, fmt.Errorf("%w: metadata", ErrServiceReceipt)
	}
	body, err := io.ReadAll(io.LimitReader(file, serviceReceiptMaxBytes+1))
	if err != nil || int64(len(body)) != before.Size {
		return serviceReceipt{}, false, fmt.Errorf("%w: read", ErrServiceReceipt)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameServiceStat(before, after) {
		return serviceReceipt{}, false, fmt.Errorf("%w: identity changed", ErrServiceReceipt)
	}
	receipt, err := parseServiceReceipt(body)
	if err != nil {
		return serviceReceipt{}, false, err
	}
	return receipt, true, nil
}

// rejectServiceDirectoryResidue accepts only an absent or empty service
// directory: anything else is residue an uninstall must resolve.
func rejectServiceDirectoryResidue(home string) error {
	directory, present, err := openServiceArtifacts(home)
	if err != nil || !present {
		return err
	}
	defer func() { _ = directory.Close() }()
	names, err := directory.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: enumerate service directory", ErrServiceResidue)
	}
	if len(names) != 0 {
		return fmt.Errorf("%w: service directory holds %s", ErrServiceResidue, names[0])
	}
	return nil
}

// receiptMatchesInstallation proves the receipt, the rendered plist, and the
// installed program agree byte-for-byte with this home and configuration.
func receiptMatchesInstallation(receipt serviceReceipt, home string, config ServiceConfig, plistPath string) error {
	if receipt.Label != config.Label {
		return fmt.Errorf("%w: receipt label %q", ErrServiceForeign, receipt.Label)
	}
	if receipt.PlistPath != plistPath {
		return fmt.Errorf("%w: receipt plist path", ErrServiceForeign)
	}
	expected, digest, err := ServicePlist(home, config.Label)
	_ = expected
	if err != nil {
		return err
	}
	if receipt.PlistDigest != hex.EncodeToString(digest[:]) {
		return fmt.Errorf("%w: receipt plist digest", ErrServiceForeign)
	}
	programDigest, err := digestServiceProgram(home)
	if err != nil {
		return err
	}
	if receipt.ProgramDigest != programDigest {
		return fmt.Errorf("%w: installed program digest", ErrServiceForeign)
	}
	return nil
}

func digestServiceProgram(home string) (string, error) {
	return digestServiceBinary(serviceProgramPath(home), "installed program")
}

// digestServiceBinary digests one binary under the exact authority rules the
// installer applies to everything it publishes: a plain file this account
// owns, executable, writable by nobody else, and byte-stable across the read.
func digestServiceBinary(path, subject string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("%w: open %s", ErrServiceAmbiguous, subject)
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 || stat.Mode&0o100 == 0 || stat.Size <= 0 || stat.Size > serviceBinaryMaxBytes {
		return "", fmt.Errorf("%w: %s metadata", ErrServiceForeign, subject)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("%w: read %s", ErrServiceAmbiguous, subject)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameServiceStat(stat, after) {
		return "", fmt.Errorf("%w: %s changed", ErrServiceForeign, subject)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// serviceSiblingDigests digests the three sibling binaries of one directory in
// serviceBinaryNames order. Comparing two of these decides whether an install
// over an existing installation is the recognized repeat or an upgrade.
func serviceSiblingDigests(directory string) ([len(serviceBinaryNames)]string, error) {
	var digests [len(serviceBinaryNames)]string
	for index, name := range serviceBinaryNames {
		digest, err := digestServiceBinary(filepath.Join(directory, name), name)
		if err != nil {
			return digests, err
		}
		digests[index] = digest
	}
	return digests, nil
}

func serviceInstall(ctx context.Context, home string, config ServiceConfig, sourceDir string) (ServiceStatus, error) {
	userHome, err := AccountHome()
	if err != nil {
		return ServiceStatus{}, errors.Join(ErrServiceAmbiguous, err)
	}
	return serviceInstallAt(ctx, home, userHome, config, sourceDir, runLaunchctl)
}

func serviceInstallAt(ctx context.Context, home, userHome string, config ServiceConfig, sourceDir string, launchctl launchctlRun) (ServiceStatus, error) {
	if ctx == nil || launchctl == nil || !config.valid() || !validServicePath(sourceDir) {
		return ServiceStatus{}, fmt.Errorf("%w: invalid install request", ErrServiceAmbiguous)
	}
	return withServiceMutation(ctx, home, func(capability *serviceHomeCapability) (ServiceStatus, error) {
		return serviceInstallLockedAt(ctx, home, userHome, config, sourceDir, launchctl, capability)
	})
}

func serviceInstallLockedAt(ctx context.Context, home, userHome string, config ServiceConfig, sourceDir string, launchctl launchctlRun, capability *serviceHomeCapability) (ServiceStatus, error) {
	inspection, err := inspectServiceWithCapabilityAt(ctx, home, userHome, config, launchctl, capability)
	status := inspection.status
	if err == nil && (status.State == ServiceInstalled || status.State == ServiceRunning) {
		// The receipt, plist, and label already prove this home's installation:
		// the only remaining question is which build it holds.
		return serviceUpgradeLockedAt(ctx, home, userHome, config, sourceDir, launchctl, inspection)
	}
	if err != nil && !errors.Is(err, ErrServiceResidue) {
		if status.State == ServiceAmbiguous {
			return status, err
		}
		return status, err
	}
	if errors.Is(err, ErrServiceResidue) {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	plistDirectory, plistPath := servicePlistLocation(userHome, config)
	if err := ensureOwnedDirectory(plistDirectory); err != nil {
		return ServiceStatus{}, err
	}
	serviceDir := ServiceDirectoryPath(home)
	for _, path := range []string{serviceDir, filepath.Join(serviceDir, "bin"), filepath.Join(serviceDir, "bin", "current")} {
		if err := ensureOwnedDirectory(path); err != nil {
			return ServiceStatus{}, err
		}
	}
	programDigest, err := publishServiceBinaries(sourceDir, filepath.Join(serviceDir, "bin", "current"))
	if err != nil {
		return ServiceStatus{}, err
	}
	plistBytes, plistDigest, err := ServicePlist(home, config.Label)
	if err != nil {
		return ServiceStatus{}, err
	}
	if err := writeExactFile(plistDirectory, config.plistName(), plistBytes, 0o600); err != nil {
		return ServiceStatus{}, err
	}
	receipt := serviceReceipt{
		Version: serviceReceiptVersion, Label: config.Label, PlistPath: plistPath,
		PlistDigest: hex.EncodeToString(plistDigest[:]), ProgramDigest: programDigest,
	}
	body, err := encodeServiceReceipt(receipt)
	if err != nil {
		return ServiceStatus{}, err
	}
	if err := writeExactFile(serviceDir, serviceReceiptName, body, 0o600); err != nil {
		return ServiceStatus{}, err
	}
	return bootstrapService(ctx, home, userHome, config, launchctl)
}

// serviceUpgradeLockedAt runs the install verb against an installation this
// home already proves is its own. The invoking factoryctl's sibling set is the
// authority: an identical set is the recognized repeat, a different one is
// moved into place — boot the loaded job out, publish the three binaries
// through the same stage-then-rename path a fresh install uses, rewrite the
// receipt's program digest, and bootstrap the new program. The plist and label
// are unchanged, so the home, its socket, database, and operator token are
// never touched.
//
// The order is what a failure leaves behind. A refused bootout replaces
// nothing. A failed bootstrap follows a receipt that already names the new
// binaries, so status stays an honest "installed" that service start resolves.
// Only a crash between the first binary and the receipt leaves the receipt
// disagreeing with the program, which is the residue service uninstall exists
// to resolve.
func serviceUpgradeLockedAt(ctx context.Context, home, userHome string, config ServiceConfig, sourceDir string, launchctl launchctlRun, inspection serviceInspection) (ServiceStatus, error) {
	serviceDir := ServiceDirectoryPath(home)
	current := filepath.Join(serviceDir, "bin", "current")
	source, err := serviceSiblingDigests(sourceDir)
	if err != nil {
		// The request is unusable; the installation is exactly as observed.
		return inspection.status, err
	}
	installed, err := serviceSiblingDigests(current)
	if err != nil {
		// The installed set cannot be read, so no present state is provable.
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	if source == installed {
		return inspection.status, nil
	}
	receipt, present, err := readServiceReceipt(home)
	if err != nil || !present {
		return ServiceStatus{State: ServiceAmbiguous}, errors.Join(ErrServiceAmbiguous, ErrServiceResidue, err)
	}
	previous, err := encodeServiceReceipt(receipt)
	if err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	if inspection.observation.present {
		if err := bootoutService(ctx, config, launchctl); err != nil {
			return ServiceStatus{State: ServiceAmbiguous}, err
		}
	}
	programDigest, err := publishServiceBinaries(sourceDir, current)
	if err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	receipt.ProgramDigest = programDigest
	body, err := encodeServiceReceipt(receipt)
	if err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	if err := replaceExactFile(serviceDir, serviceReceiptName, previous, body, 0o600); err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	return bootstrapService(ctx, home, userHome, config, launchctl)
}

// publishServiceBinaries copies the three sibling binaries into the service
// directory and returns factoryd's digest, which is the receipt's program
// identity.
func publishServiceBinaries(sourceDir, destinationDir string) (string, error) {
	var programDigest string
	for _, name := range serviceBinaryNames {
		digest, err := copyServiceBinary(filepath.Join(sourceDir, name), destinationDir, name)
		if err != nil {
			return "", err
		}
		if name == "factoryd" {
			programDigest = digest
		}
	}
	return programDigest, nil
}

// bootstrapService loads the retained plist and confirms the result. Install,
// upgrade, and start all reach launchd through exactly this one path.
func bootstrapService(ctx context.Context, home, userHome string, config ServiceConfig, launchctl launchctlRun) (ServiceStatus, error) {
	_, plistPath := servicePlistLocation(userHome, config)
	result := launchctl(ctx, "bootstrap", "gui/"+strconv.Itoa(os.Geteuid()), plistPath)
	if result.err != nil || result.status != 0 {
		return ServiceStatus{State: ServiceInstalled}, fmt.Errorf("%w: bootstrap status %d: %v", ErrServiceLaunchctl, result.status, result.err)
	}
	return confirmServiceLoaded(ctx, home, config, plistPath, launchctl)
}

func serviceStart(ctx context.Context, home string, config ServiceConfig) (ServiceStatus, error) {
	userHome, err := AccountHome()
	if err != nil {
		return ServiceStatus{}, errors.Join(ErrServiceAmbiguous, err)
	}
	return serviceStartAt(ctx, home, userHome, config, runLaunchctl)
}

func serviceStartAt(ctx context.Context, home, userHome string, config ServiceConfig, launchctl launchctlRun) (ServiceStatus, error) {
	if ctx == nil || launchctl == nil || !config.valid() {
		return ServiceStatus{}, fmt.Errorf("%w: invalid start request", ErrServiceAmbiguous)
	}
	return withServiceMutation(ctx, home, func(capability *serviceHomeCapability) (ServiceStatus, error) {
		return serviceStartLockedAt(ctx, home, userHome, config, launchctl, capability)
	})
}

func serviceStartLockedAt(ctx context.Context, home, userHome string, config ServiceConfig, launchctl launchctlRun, capability *serviceHomeCapability) (ServiceStatus, error) {
	inspection, err := inspectServiceWithCapabilityAt(ctx, home, userHome, config, launchctl, capability)
	status := inspection.status
	if err != nil {
		return status, err
	}
	switch status.State {
	case ServiceRunning:
		return status, nil
	case ServiceInstalled:
		if inspection.observation.present {
			// RunAtLoad without KeepAlive can leave an exited job loaded. Remove
			// that definition before reusing the one bootstrap path below.
			if err := bootoutService(ctx, config, launchctl); err != nil {
				return ServiceStatus{State: ServiceAmbiguous}, err
			}
		}
	default:
		return status, fmt.Errorf("%w: start requires an installed service", ErrServiceAmbiguous)
	}
	return bootstrapService(ctx, home, userHome, config, launchctl)
}

func serviceStop(ctx context.Context, home string, config ServiceConfig) (ServiceStatus, error) {
	userHome, err := AccountHome()
	if err != nil {
		return ServiceStatus{}, errors.Join(ErrServiceAmbiguous, err)
	}
	return serviceStopAt(ctx, home, userHome, config, runLaunchctl)
}

func serviceStopAt(ctx context.Context, home, userHome string, config ServiceConfig, launchctl launchctlRun) (ServiceStatus, error) {
	if ctx == nil || launchctl == nil || !config.valid() {
		return ServiceStatus{}, fmt.Errorf("%w: invalid stop request", ErrServiceAmbiguous)
	}
	return withServiceMutation(ctx, home, func(capability *serviceHomeCapability) (ServiceStatus, error) {
		return serviceStopLockedAt(ctx, home, userHome, config, launchctl, capability)
	})
}

func serviceStopLockedAt(ctx context.Context, home, userHome string, config ServiceConfig, launchctl launchctlRun, capability *serviceHomeCapability) (ServiceStatus, error) {
	inspection, err := inspectServiceWithCapabilityAt(ctx, home, userHome, config, launchctl, capability)
	status := inspection.status
	if err != nil {
		return status, err
	}
	if status.State == ServiceInstalled && !inspection.observation.present {
		return status, nil
	}
	if status.State != ServiceInstalled && status.State != ServiceRunning {
		return status, fmt.Errorf("%w: stop requires an installed service", ErrServiceAmbiguous)
	}
	if err := bootoutService(ctx, config, launchctl); err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	return ServiceStatus{State: ServiceInstalled}, nil
}

func serviceUninstall(ctx context.Context, home string, config ServiceConfig) (ServiceStatus, error) {
	userHome, err := AccountHome()
	if err != nil {
		return ServiceStatus{}, errors.Join(ErrServiceAmbiguous, err)
	}
	return serviceUninstallAt(ctx, home, userHome, config, runLaunchctl)
}

// serviceUninstallAt removes exactly this installation's artifacts and is the
// resolution path for crash residue, including its own stage files. It is
// evidence-first: no mutating launchctl verb runs until a matching receipt or
// an exactly rendered plist proves the label maps to this home, and it never
// deletes bytes it cannot prove are its own property.
func serviceUninstallAt(ctx context.Context, home, userHome string, config ServiceConfig, launchctl launchctlRun) (ServiceStatus, error) {
	if ctx == nil || launchctl == nil || !config.valid() || !validServicePath(home) {
		return ServiceStatus{}, fmt.Errorf("%w: invalid uninstall request", ErrServiceAmbiguous)
	}
	return withServiceMutation(ctx, home, func(*serviceHomeCapability) (ServiceStatus, error) {
		return serviceUninstallLockedAt(ctx, home, userHome, config, launchctl)
	})
}

func serviceUninstallLockedAt(ctx context.Context, home, userHome string, config ServiceConfig, launchctl launchctlRun) (ServiceStatus, error) {
	plistDirectory, plistPath := servicePlistLocation(userHome, config)
	expectedPlist, expectedPlistDigest, err := ServicePlist(home, config.Label)
	if err != nil {
		return ServiceStatus{}, err
	}
	receipt, receiptPresent, receiptErr := readServiceReceipt(home)
	if receiptErr == nil && receiptPresent {
		if receipt.Label != config.Label || receipt.PlistPath != plistPath || receipt.PlistDigest != hex.EncodeToString(expectedPlistDigest[:]) {
			// The service directory belongs to a different installation target;
			// removing it here would orphan that installation.
			return ServiceStatus{State: ServiceAmbiguous}, fmt.Errorf("%w: the receipt names a different installation target", ErrServiceForeign)
		}
	}
	evidence := receiptErr == nil && receiptPresent
	if !evidence {
		plistEvidence, err := uninstallPlistEvidence(home, config, plistDirectory)
		if err != nil {
			// A foreign plist refuses before any mutation, launchctl included.
			return ServiceStatus{State: ServiceAmbiguous}, err
		}
		evidence = plistEvidence
	}
	if evidence {
		service := "gui/" + strconv.Itoa(os.Geteuid()) + "/" + config.Label
		observation, err := observeLaunchctl(ctx, launchctl, service, plistPath, serviceProgramPath(home))
		if err != nil {
			return ServiceStatus{State: ServiceAmbiguous}, err
		}
		if observation.present {
			if err := bootoutService(ctx, config, launchctl); err != nil {
				return ServiceStatus{State: ServiceAmbiguous}, err
			}
		}
	}
	if err := removeExactFile(plistDirectory, config.plistName(), expectedPlist); err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	if err := removeOwnedFile(plistDirectory, "."+config.plistName()+".stage"); err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	directory, present, err := openServiceArtifacts(home)
	if err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	if present {
		defer func() { _ = directory.Close() }()
		current := filepath.Join(ServiceDirectoryPath(home), "bin", "current")
		for _, name := range serviceBinaryNames {
			if err := removeOwnedFile(current, name); err != nil {
				return ServiceStatus{State: ServiceAmbiguous}, err
			}
			// Exactly this engine's own stage names are crash residue it must
			// resolve; nothing else in the tree is deletable without proof.
			if err := removeOwnedFile(current, "."+name+".stage"); err != nil {
				return ServiceStatus{State: ServiceAmbiguous}, err
			}
		}
		for _, path := range []string{current, filepath.Join(ServiceDirectoryPath(home), "bin")} {
			if err := removeEmptyOwnedDirectory(path); err != nil {
				return ServiceStatus{State: ServiceAmbiguous}, err
			}
		}
		for _, name := range []string{serviceReceiptName, "." + serviceReceiptName + ".stage"} {
			if err := removeOwnedFile(ServiceDirectoryPath(home), name); err != nil {
				return ServiceStatus{State: ServiceAmbiguous}, err
			}
		}
		if err := directory.Close(); err != nil {
			return ServiceStatus{State: ServiceAmbiguous}, err
		}
		if err := removeEmptyOwnedDirectory(ServiceDirectoryPath(home)); err != nil {
			return ServiceStatus{State: ServiceAmbiguous}, err
		}
	}
	return ServiceStatus{State: ServiceAbsent}, nil
}

// uninstallPlistEvidence proves label-to-home ownership from the plist alone:
// exact rendered bytes are evidence, absence is no evidence, and any other
// bytes refuse the whole uninstall before a single mutation.
func uninstallPlistEvidence(home string, config ServiceConfig, plistDirectory string) (bool, error) {
	expected, _, err := ServicePlist(home, config.Label)
	if err != nil {
		return false, err
	}
	path := filepath.Join(plistDirectory, config.plistName())
	fd, openErr := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(openErr, unix.ENOENT) {
		return false, nil
	}
	if openErr != nil {
		return false, fmt.Errorf("%w: probe plist", ErrServiceAmbiguous)
	}
	file := os.NewFile(uintptr(fd), config.plistName())
	body, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return false, fmt.Errorf("%w: read plist", ErrServiceAmbiguous)
	}
	if bytes.Equal(body, expected) {
		return true, nil
	}
	return false, fmt.Errorf("%w: the plist at %s is not this installation's property", ErrServiceForeign, config.plistName())
}

func servicePlistLocation(userHome string, config ServiceConfig) (directory, path string) {
	directory = config.PlistDirectory
	if directory == "" {
		directory = filepath.Join(userHome, "Library", "LaunchAgents")
	}
	return directory, filepath.Join(directory, config.plistName())
}

func confirmServiceLoaded(ctx context.Context, home string, config ServiceConfig, plistPath string, launchctl launchctlRun) (ServiceStatus, error) {
	service := "gui/" + strconv.Itoa(os.Geteuid()) + "/" + config.Label
	deadline := time.Now().Add(serviceBootoutPatience)
	for {
		observation, err := observeLaunchctl(ctx, launchctl, service, plistPath, serviceProgramPath(home))
		if err == nil {
			if !observation.present {
				return ServiceStatus{State: ServiceInstalled}, fmt.Errorf("%w: job absent after bootstrap", ErrServiceLaunchctl)
			}
			if observation.pid > 0 {
				return ServiceStatus{State: ServiceRunning, PID: observation.pid}, nil
			}
			return ServiceStatus{State: ServiceInstalled}, nil
		}
		// launchd passes through transient spawn states whose print shapes the
		// strict parser refuses; each observation stays exact, the confirmation
		// merely waits out the transient within one bound.
		if time.Now().After(deadline) {
			return ServiceStatus{State: ServiceAmbiguous}, err
		}
		select {
		case <-ctx.Done():
			return ServiceStatus{State: ServiceAmbiguous}, ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func bootoutService(ctx context.Context, config ServiceConfig, launchctl launchctlRun) error {
	service := "gui/" + strconv.Itoa(os.Geteuid()) + "/" + config.Label
	result := launchctl(ctx, "bootout", service)
	if result.err != nil {
		return errors.Join(ErrServiceLaunchctl, result.err)
	}
	if result.status != 0 && result.status != launchctlNotFound && result.status != int(unix.EINPROGRESS) {
		return fmt.Errorf("%w: bootout status %d", ErrServiceLaunchctl, result.status)
	}
	deadline := time.Now().Add(serviceBootoutPatience)
	for {
		probe := launchctl(ctx, "print", service)
		if probe.err == nil && probe.status == launchctlNotFound && validNotFoundStderr(probe.stderr, service) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: job survived bootout", ErrServiceLaunchctl)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func ensureOwnedDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: create %s: %v", ErrServiceAmbiguous, filepath.Base(path), err)
	}
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 {
		return fmt.Errorf("%w: directory authority %s", ErrServiceForeign, filepath.Base(path))
	}
	return nil
}

func copyServiceBinary(sourcePath, destinationDir, name string) (string, error) {
	sourceFD, err := unix.Open(sourcePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("%w: open source %s", ErrServiceAmbiguous, name)
	}
	source := os.NewFile(uintptr(sourceFD), name)
	defer func() { _ = source.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(sourceFD, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 || stat.Mode&0o100 == 0 || stat.Size <= 0 || stat.Size > serviceBinaryMaxBytes {
		return "", fmt.Errorf("%w: source binary %s", ErrServiceForeign, name)
	}
	stageName := "." + name + ".stage"
	stagePath := filepath.Join(destinationDir, stageName)
	destination, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("%w: stale %s stage; run factoryctl service uninstall", ErrServiceResidue, name)
	}
	if err != nil {
		return "", fmt.Errorf("%w: stage %s: %v", ErrServiceAmbiguous, name, err)
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(destination, digest), source)
	syncErr := destination.Sync()
	closeErr := destination.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(stagePath)
		return "", fmt.Errorf("%w: copy %s", ErrServiceAmbiguous, name)
	}
	var after unix.Stat_t
	if err := unix.Fstat(sourceFD, &after); err != nil || !sameServiceStat(stat, after) {
		_ = os.Remove(stagePath)
		return "", fmt.Errorf("%w: source binary %s changed", ErrServiceForeign, name)
	}
	if err := os.Rename(stagePath, filepath.Join(destinationDir, name)); err != nil {
		_ = os.Remove(stagePath)
		return "", fmt.Errorf("%w: publish %s", ErrServiceAmbiguous, name)
	}
	if err := syncServiceDirectory(destinationDir); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// writeExactFile publishes durable bytes that must not already exist as
// anything else: a file already holding exactly these bytes is the recognized
// repeat, and any other existing bytes are foreign and refuse.
func writeExactFile(directory, name string, contents []byte, mode os.FileMode) error {
	existing, present, err := readExistingFile(directory, name, int64(len(contents))+1)
	if err != nil {
		return err
	}
	if present {
		if bytes.Equal(existing, contents) {
			return nil
		}
		return fmt.Errorf("%w: %s exists with different bytes", ErrServiceForeign, name)
	}
	return publishStagedFile(directory, name, contents, mode)
}

// replaceExactFile rewrites one durable file this installation already owns.
// The bytes on disk must be exactly previous — the proof the file is this
// installation's property — and the replacement lands through the same
// stage-then-rename publication, so an interruption leaves either the old file
// or the new one and never a partial write.
func replaceExactFile(directory, name string, previous, contents []byte, mode os.FileMode) error {
	limit := int64(len(previous))
	if int64(len(contents)) > limit {
		limit = int64(len(contents))
	}
	existing, present, err := readExistingFile(directory, name, limit+1)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("%w: %s is missing; run factoryctl service uninstall", ErrServiceResidue, name)
	}
	if bytes.Equal(existing, contents) {
		return nil
	}
	if !bytes.Equal(existing, previous) {
		return fmt.Errorf("%w: %s holds different bytes", ErrServiceForeign, name)
	}
	return publishStagedFile(directory, name, contents, mode)
}

// readExistingFile reads a member's bytes up to limit. Absence is an ordinary
// result, never an error.
func readExistingFile(directory, name string, limit int64) ([]byte, bool, error) {
	fd, err := unix.Open(filepath.Join(directory, name), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: probe %s", ErrServiceAmbiguous, name)
	}
	file := os.NewFile(uintptr(fd), name)
	existing, readErr := io.ReadAll(io.LimitReader(file, limit))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, false, fmt.Errorf("%w: read existing %s", ErrServiceAmbiguous, name)
	}
	return existing, true, nil
}

func publishStagedFile(directory, name string, contents []byte, mode os.FileMode) error {
	stagePath := filepath.Join(directory, "."+name+".stage")
	file, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%w: stale %s stage; run factoryctl service uninstall", ErrServiceResidue, name)
	}
	if err != nil {
		return fmt.Errorf("%w: stage %s: %v", ErrServiceAmbiguous, name, err)
	}
	_, writeErr := file.Write(contents)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(stagePath)
		return fmt.Errorf("%w: write %s", ErrServiceAmbiguous, name)
	}
	if err := os.Rename(stagePath, filepath.Join(directory, name)); err != nil {
		_ = os.Remove(stagePath)
		return fmt.Errorf("%w: publish %s", ErrServiceAmbiguous, name)
	}
	return syncServiceDirectory(directory)
}

func removeExactFile(directory, name string, expected []byte) error {
	path := filepath.Join(directory, name)
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: open %s", ErrServiceAmbiguous, name)
	}
	file := os.NewFile(uintptr(fd), name)
	body, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("%w: read %s", ErrServiceAmbiguous, name)
	}
	if !bytes.Equal(body, expected) {
		return fmt.Errorf("%w: %s holds different bytes; refusing removal", ErrServiceForeign, name)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove %s", ErrServiceAmbiguous, name)
	}
	return syncServiceDirectory(directory)
}

func removeOwnedFile(directory, name string) error {
	path := filepath.Join(directory, name)
	var stat unix.Stat_t
	err := unix.Lstat(path, &stat)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: probe %s", ErrServiceAmbiguous, name)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: %s is not this installation's file", ErrServiceForeign, name)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove %s", ErrServiceAmbiguous, name)
	}
	return nil
}

func removeEmptyOwnedDirectory(path string) error {
	var stat unix.Stat_t
	err := unix.Lstat(path, &stat)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: directory %s", ErrServiceForeign, filepath.Base(path))
	}
	if err := unix.Rmdir(path); err != nil {
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%w: %s is not empty; refusing removal", ErrServiceForeign, filepath.Base(path))
		}
		return fmt.Errorf("%w: remove directory %s", ErrServiceAmbiguous, filepath.Base(path))
	}
	return nil
}

func syncServiceDirectory(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("%w: open directory for sync", ErrServiceAmbiguous)
	}
	syncErr := unix.Fsync(fd)
	closeErr := unix.Close(fd)
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("%w: sync directory", ErrServiceAmbiguous)
	}
	return nil
}
