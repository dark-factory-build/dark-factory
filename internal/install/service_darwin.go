//go:build darwin

package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	launchctlPath         = "/bin/launchctl"
	launchctlNotFound     = 113
	launchctlNotFoundText = "113: Could not find specified service"
	launchctlOutputLimit  = 64 << 10
	launchctlTimeout      = 3 * time.Second
	servicePlistName      = serviceLabel + ".plist"
)

type launchctlResult struct {
	stdout   []byte
	stderr   []byte
	status   int
	err      error
	overflow bool
}

type launchctlRun func(context.Context, ...string) launchctlResult

type boundedCommandBuffer struct {
	buffer   bytes.Buffer
	overflow bool
}

func (buffer *boundedCommandBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := launchctlOutputLimit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return written, nil
	}
	if len(value) > remaining {
		buffer.overflow = true
		value = value[:remaining]
	}
	_, _ = buffer.buffer.Write(value)
	return written, nil
}

func runLaunchctl(ctx context.Context, arguments ...string) launchctlResult {
	return runLaunchctlBinary(ctx, launchctlPath, arguments...)
}

func runLaunchctlBinary(ctx context.Context, binary string, arguments ...string) launchctlResult {
	if ctx == nil {
		return launchctlResult{status: -1, err: errors.New("nil launchctl context")}
	}
	callContext, cancel := context.WithTimeout(ctx, launchctlTimeout)
	defer cancel()
	command := exec.CommandContext(callContext, binary, arguments...)
	command.Env = []string{"LANG=C", "LC_ALL=C", "PATH=/usr/bin:/bin"}
	command.WaitDelay = 250 * time.Millisecond
	var stdout, stderr boundedCommandBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := launchctlResult{
		stdout: stdout.buffer.Bytes(), stderr: stderr.buffer.Bytes(), status: 0,
		err: err, overflow: stdout.overflow || stderr.overflow,
	}
	if err == nil {
		return result
	}
	if callContext.Err() != nil {
		result.err = callContext.Err()
		result.status = -1
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ProcessState != nil {
		result.status = exit.ExitCode()
		result.err = nil
		return result
	}
	result.status = -1
	return result
}

type launchctlObservation struct {
	present bool
	pid     int
}

func inspectService(ctx context.Context, home, userHome string, launchctl launchctlRun) (status ServiceStatus, resultErr error) {
	if ctx == nil || launchctl == nil {
		return ServiceStatus{}, fmt.Errorf("%w: invalid status request", ErrServiceAmbiguous)
	}
	if err := inspectServiceHome(ctx, home); err != nil {
		return ServiceStatus{}, err
	}
	userDirectory, err := openServiceDirectory(userHome)
	if err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, fmt.Errorf("%w: user home", ErrServiceAmbiguous)
	}
	defer func() {
		if closeErr := userDirectory.close(); closeErr != nil {
			resultErr = errors.Join(resultErr, ErrServiceAmbiguous, closeErr)
		}
	}()
	plistPresent, err := inspectServicePlist(userDirectory, home)
	if err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, errors.Join(ErrServiceAmbiguous, err)
	}
	if err := userDirectory.recheck(); err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, errors.Join(ErrServiceAmbiguous, err)
	}

	uid := os.Geteuid()
	service := "gui/" + strconv.Itoa(uid) + "/" + serviceLabel
	plistPath := filepath.Join(userHome, "Library", "LaunchAgents", servicePlistName)
	programPath := filepath.Join(home, "bin", "current", "factoryd")
	observation, err := observeLaunchctl(ctx, launchctl, service, plistPath, programPath)
	if err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, err
	}
	secondPlistPresent, err := inspectServicePlist(userDirectory, home)
	if err != nil || secondPlistPresent != plistPresent {
		return ServiceStatus{State: ServiceAmbiguous}, errors.Join(ErrServiceAmbiguous, err)
	}
	if err := userDirectory.recheck(); err != nil {
		return ServiceStatus{State: ServiceAmbiguous}, errors.Join(ErrServiceAmbiguous, err)
	}
	if !plistPresent && !observation.present {
		return ServiceStatus{State: ServiceAbsent}, nil
	}
	return ServiceStatus{State: ServiceAmbiguous, PID: observation.pid}, ErrServiceAmbiguous
}

func inspectServiceHome(ctx context.Context, path string) (resultErr error) {
	if ctx == nil {
		return context.Canceled
	}
	parentPath, base, err := splitHome(path)
	if err != nil {
		return err
	}
	parent, err := openParent(parentPath)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, parent.close()) }()
	if err := rejectIfPresent(parent.file, "."+base+stageSuffix, "staging path"); err != nil {
		return err
	}
	var expected unix.Stat_t
	var first serviceHomeImage
	for pass := 0; pass < 2; pass++ {
		home, stat, err := openDirectoryMember(parent.file, base)
		if err != nil {
			return fmt.Errorf("open service home: %w", err)
		}
		if pass == 0 {
			expected = stat
		} else if !sameServiceStat(expected, stat) {
			_ = home.Close()
			return fmt.Errorf("%w: service home identity changed", ErrInvalidHome)
		}
		image, inspectErr := snapshotServiceHome(ctx, home)
		if inspectErr == nil && pass == 0 {
			first = image
		} else if inspectErr == nil {
			inspectErr = sameServiceHomeImage(first, image)
		}
		if inspectErr == nil {
			inspectErr = recheckBinding(parent.file, base, stat)
		}
		closeErr := home.Close()
		if inspectErr != nil || closeErr != nil {
			return errors.Join(inspectErr, closeErr)
		}
		if err := parent.recheck(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

type serviceHomeImage struct {
	root        identity
	files       map[string]memberSnapshot
	directories map[string]identity
}

func snapshotServiceHome(ctx context.Context, home *os.File) (serviceHomeImage, error) {
	if ctx == nil {
		return serviceHomeImage{}, context.Canceled
	}
	var rootBefore unix.Stat_t
	if err := unix.Fstat(int(home.Fd()), &rootBefore); err != nil {
		return serviceHomeImage{}, err
	}
	if err := exactDirectory(home, false); err != nil {
		return serviceHomeImage{}, err
	}
	names, err := readOperationalCensus(home)
	if err != nil {
		return serviceHomeImage{}, err
	}
	if err := inspectFile(ctx, home, formatName, []byte(formatBytes)); err != nil {
		return serviceHomeImage{}, err
	}
	if err := inspectToken(home); err != nil {
		return serviceHomeImage{}, err
	}
	if err := inspectLockPair(home); err != nil {
		return serviceHomeImage{}, err
	}
	image := serviceHomeImage{root: toIdentity(rootBefore), files: make(map[string]memberSnapshot, len(names)-2), directories: make(map[string]identity, 2)}
	for _, name := range []string{formatName, databaseName, tokenName, lockName, lockAnchorName, databaseName + "-wal", databaseName + "-shm"} {
		if !names[name] {
			continue
		}
		var file *os.File
		var stat unix.Stat_t
		var err error
		if name == lockName || name == lockAnchorName {
			file, stat, err = openLockMember(home, name)
		} else {
			file, stat, err = openMember(home, name)
		}
		if err != nil {
			return serviceHomeImage{}, err
		}
		minimum, maximum := int64(0), int64(0)
		switch name {
		case formatName:
			minimum, maximum = int64(len(formatBytes)), int64(len(formatBytes))
		case tokenName:
			minimum, maximum = operatorTokenBytes, operatorTokenBytes
		case lockName, lockAnchorName:
			minimum, maximum = 0, 0
		case databaseName:
			minimum, maximum = 100, operationalMaxDatabaseBytes
		default:
			minimum, maximum = operationalSidecarBounds(name)
		}
		digest, digestErr := digestMember(ctx, file, stat.Size, minimum, maximum)
		bindingErr := recheckBinding(home, name, stat)
		closeErr := file.Close()
		if digestErr != nil || bindingErr != nil || closeErr != nil {
			return serviceHomeImage{}, errors.Join(digestErr, bindingErr, closeErr)
		}
		image.files[name] = memberSnapshot{identity: toIdentity(stat), digest: digest}
	}
	for _, name := range []string{runtimesName, changesName} {
		directory, stat, err := openDirectoryMember(home, name)
		if err != nil {
			return serviceHomeImage{}, err
		}
		closeErr := directory.Close()
		if closeErr != nil {
			return serviceHomeImage{}, closeErr
		}
		image.directories[name] = toIdentity(stat)
	}
	var rootAfter unix.Stat_t
	if err := unix.Fstat(int(home.Fd()), &rootAfter); err != nil {
		return serviceHomeImage{}, err
	}
	if err := sameIdentities(toIdentity(rootBefore), toIdentity(rootAfter)); err != nil {
		return serviceHomeImage{}, err
	}
	if err := ctx.Err(); err != nil {
		return serviceHomeImage{}, err
	}
	return image, nil
}

func sameServiceHomeImage(left, right serviceHomeImage) error {
	if err := sameIdentities(left.root, right.root); err != nil {
		return err
	}
	if len(left.files) != len(right.files) || len(left.directories) != len(right.directories) {
		return fmt.Errorf("%w: service home census changed", ErrInvalidHome)
	}
	for name, expected := range left.files {
		if right.files[name] != expected {
			return fmt.Errorf("%w: service home member changed", ErrInvalidHome)
		}
	}
	for name, expected := range left.directories {
		if right.directories[name] != expected {
			return fmt.Errorf("%w: service home directory changed", ErrInvalidHome)
		}
	}
	return nil
}

func observeLaunchctl(ctx context.Context, launchctl launchctlRun, service, plistPath, programPath string) (launchctlObservation, error) {
	result := launchctl(ctx, "print", service)
	if result.overflow {
		return launchctlObservation{}, fmt.Errorf("%w: output exceeded bound", ErrServiceLaunchctl)
	}
	if result.err != nil {
		return launchctlObservation{}, errors.Join(ErrServiceLaunchctl, result.err)
	}
	if result.status == 0 {
		if len(bytes.TrimSpace(result.stderr)) != 0 {
			return launchctlObservation{}, fmt.Errorf("%w: successful print carried stderr", ErrServiceLaunchctl)
		}
		pid, err := parseLaunchctlPrint(result.stdout, service, plistPath, programPath)
		if err != nil {
			return launchctlObservation{}, err
		}
		return launchctlObservation{present: true, pid: pid}, nil
	}
	if result.status != launchctlNotFound {
		return launchctlObservation{}, fmt.Errorf("%w: print status %d", ErrServiceLaunchctl, result.status)
	}
	classification := launchctl(ctx, "error", strconv.Itoa(result.status))
	if classification.overflow || classification.err != nil || classification.status != 0 || strings.TrimSpace(string(classification.stdout)) != launchctlNotFoundText || len(bytes.TrimSpace(classification.stderr)) != 0 {
		if classification.err != nil {
			return launchctlObservation{}, errors.Join(ErrServiceLaunchctl, classification.err)
		}
		return launchctlObservation{}, fmt.Errorf("%w: not-found classification mismatch", ErrServiceLaunchctl)
	}
	return launchctlObservation{}, nil
}

func parseLaunchctlPrint(output []byte, service, plistPath, programPath string) (int, error) {
	if len(output) == 0 || len(output) > launchctlOutputLimit || !utf8.Valid(output) || bytes.IndexByte(output, 0) >= 0 {
		return 0, fmt.Errorf("%w: malformed print output", ErrServiceLaunchctl)
	}
	lines := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != service+" = {" || strings.TrimSpace(lines[len(lines)-1]) != "}" {
		return 0, fmt.Errorf("%w: malformed service envelope", ErrServiceLaunchctl)
	}
	fields := make(map[string]string, 4)
	for _, line := range lines[1 : len(lines)-1] {
		trimmed := strings.TrimSpace(line)
		for _, key := range []string{"path", "state", "program", "pid"} {
			prefix := key + " = "
			if !strings.HasPrefix(trimmed, prefix) {
				continue
			}
			if _, duplicate := fields[key]; duplicate {
				return 0, fmt.Errorf("%w: duplicate %s", ErrServiceLaunchctl, key)
			}
			fields[key] = strings.TrimPrefix(trimmed, prefix)
		}
	}
	if fields["path"] != plistPath || fields["program"] != programPath {
		return 0, fmt.Errorf("%w: foreign launchd ownership", ErrServiceLaunchctl)
	}
	switch fields["state"] {
	case "running":
		pid, err := strconv.ParseInt(fields["pid"], 10, 32)
		if err != nil || pid <= 1 {
			return 0, fmt.Errorf("%w: invalid launchd pid", ErrServiceLaunchctl)
		}
		return int(pid), nil
	case "not running":
		if _, present := fields["pid"]; present {
			return 0, fmt.Errorf("%w: stopped service carries pid", ErrServiceLaunchctl)
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("%w: unknown launchd state", ErrServiceLaunchctl)
	}
}

type serviceDirectory struct {
	files []*os.File
	names []string
	stats []unix.Stat_t
}

func openServiceDirectory(path string) (*serviceDirectory, error) {
	if !validServicePath(path) || path == "/" {
		return nil, errors.New("invalid service directory path")
	}
	rootFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(rootFD), "/")
	if root == nil {
		_ = unix.Close(rootFD)
		return nil, errors.New("invalid service root descriptor")
	}
	directory := &serviceDirectory{files: []*os.File{root}, names: []string{""}}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, name := range parts {
		if name == "" || name == "." || name == ".." || len(name) > maxNameSize {
			_ = directory.close()
			return nil, errors.New("invalid service directory component")
		}
		fd, openErr := unix.Openat(int(directory.files[len(directory.files)-1].Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = directory.close()
			return nil, openErr
		}
		file := os.NewFile(uintptr(fd), name)
		if file == nil {
			_ = unix.Close(fd)
			_ = directory.close()
			return nil, errors.New("invalid service directory descriptor")
		}
		directory.files = append(directory.files, file)
		directory.names = append(directory.names, name)
	}
	for _, file := range directory.files {
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
			_ = directory.close()
			return nil, err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Nlink < 2 {
			_ = directory.close()
			return nil, errors.New("service path component is not a stable directory")
		}
		directory.stats = append(directory.stats, stat)
	}
	final := directory.stats[len(directory.stats)-1]
	if final.Uid != uint32(os.Geteuid()) || final.Mode&0o022 != 0 {
		_ = directory.close()
		return nil, errors.New("service directory is not owned and protected")
	}
	if err := directory.recheck(); err != nil {
		_ = directory.close()
		return nil, err
	}
	return directory, nil
}

func (directory *serviceDirectory) recheck() error {
	for index, file := range directory.files {
		var current unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &current); err != nil {
			return err
		}
		if !sameServiceStat(directory.stats[index], current) {
			return errors.New("service directory identity changed")
		}
		if index == 0 {
			continue
		}
		var binding unix.Stat_t
		if err := unix.Fstatat(int(directory.files[index-1].Fd()), directory.names[index], &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameServiceStat(directory.stats[index], binding) {
			return errors.New("service directory binding changed")
		}
	}
	return nil
}

func (directory *serviceDirectory) close() error {
	var result error
	for index := len(directory.files) - 1; index >= 0; index-- {
		result = errors.Join(result, directory.files[index].Close())
	}
	return result
}

func sameServiceStat(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode && left.Uid == right.Uid && left.Gid == right.Gid
}

func inspectServicePlist(userHome *serviceDirectory, home string) (present bool, resultErr error) {
	parent := userHome.files[len(userHome.files)-1]
	var children []*os.File
	defer func() {
		for index := len(children) - 1; index >= 0; index-- {
			if closeErr := children[index].Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, ErrServicePlist, closeErr)
			}
		}
	}()
	for _, name := range []string{"Library", "LaunchAgents"} {
		fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%w: plist parent", ErrServicePlist)
		}
		child := os.NewFile(uintptr(fd), name)
		if child == nil {
			_ = unix.Close(fd)
			return false, fmt.Errorf("%w: invalid plist parent descriptor", ErrServicePlist)
		}
		children = append(children, child)
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o022 != 0 {
			return false, fmt.Errorf("%w: plist parent authority", ErrServicePlist)
		}
		parent = child
	}
	fd, err := unix.Openat(int(parent.Fd()), servicePlistName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: open plist", ErrServicePlist)
	}
	file := os.NewFile(uintptr(fd), servicePlistName)
	if file == nil {
		_ = unix.Close(fd)
		return false, fmt.Errorf("%w: invalid plist descriptor", ErrServicePlist)
	}
	children = append(children, file)
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0o7777 != 0o600 || before.Uid != uint32(os.Geteuid()) || before.Nlink != 1 || before.Size <= 0 || before.Size > launchctlOutputLimit {
		return false, fmt.Errorf("%w: plist metadata", ErrServicePlist)
	}
	expected, _, err := ServicePlist(home)
	if err != nil || int64(len(expected)) != before.Size {
		return false, fmt.Errorf("%w: plist size", ErrServicePlist)
	}
	body, err := io.ReadAll(io.LimitReader(file, launchctlOutputLimit+1))
	if err != nil || !bytes.Equal(body, expected) {
		return false, fmt.Errorf("%w: plist bytes", ErrServicePlist)
	}
	var after, binding unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameServiceStat(before, after) {
		return false, fmt.Errorf("%w: plist identity changed", ErrServicePlist)
	}
	if err := unix.Fstatat(int(parent.Fd()), servicePlistName, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameServiceStat(before, binding) {
		return false, fmt.Errorf("%w: plist binding changed", ErrServicePlist)
	}
	return true, nil
}
