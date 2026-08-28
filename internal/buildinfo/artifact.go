package buildinfo

import (
	"bytes"
	debugbuildinfo "debug/buildinfo"
	"debug/macho"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	MaxBinaryBytes         int64 = 64 << 20
	MaxTargetBytes         int64 = 192 << 20
	MaxArchiveBytes        int64 = 128 << 20
	MaxReleaseArchiveBytes int64 = 256 << 20
	MaxCompressionRatio    int64 = 64
)

// SnapshotReleaseArtifact opens one source without following a final symlink,
// copies from that retained descriptor, and verifies the private snapshot. A
// later replacement of the source pathname cannot change the packaged bytes.
func SnapshotReleaseArtifact(source, destination, component string, expected Identity) (Identity, error) {
	return snapshotReleaseArtifact(source, destination, component, expected, nil)
}

func snapshotReleaseArtifact(source, destination, component string, expected Identity, afterOpen func()) (_ Identity, result error) {
	if !expected.Release() || !validComponent(component) || filepath.Base(source) != component || filepath.Base(destination) != component {
		return Identity{}, errors.New("invalid release artifact request")
	}
	descriptor, err := unix.Open(source, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return Identity{}, fmt.Errorf("open release artifact: %w", err)
	}
	input := os.NewFile(uintptr(descriptor), source)
	defer func() {
		if closeErr := input.Close(); result == nil && closeErr != nil {
			result = fmt.Errorf("close release artifact: %w", closeErr)
		}
	}()
	before, err := artifactFacts(input)
	if err != nil {
		return Identity{}, err
	}
	if afterOpen != nil {
		afterOpen()
	}

	output, err := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Identity{}, fmt.Errorf("create release snapshot: %w", err)
	}
	keep := false
	defer func() {
		if closeErr := output.Close(); result == nil && closeErr != nil {
			result = fmt.Errorf("close release snapshot: %w", closeErr)
		}
		if !keep {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.CopyN(output, input, before.size); err != nil {
		return Identity{}, fmt.Errorf("copy release artifact: %w", err)
	}
	var trailing [1]byte
	if count, err := input.Read(trailing[:]); count != 0 || err != io.EOF {
		if err == nil {
			err = errors.New("source grew while copying")
		}
		return Identity{}, fmt.Errorf("copy release artifact: %w", err)
	}
	after, err := artifactFacts(input)
	if err != nil || before != after {
		return Identity{}, errors.New("release artifact changed while copying")
	}
	if err := output.Chmod(0o755); err != nil {
		return Identity{}, fmt.Errorf("set release snapshot mode: %w", err)
	}
	if err := output.Sync(); err != nil {
		return Identity{}, fmt.Errorf("sync release snapshot: %w", err)
	}
	identity, err := InspectReleaseArtifact(output, component, expected)
	if err != nil {
		return Identity{}, err
	}
	keep = true
	return identity, nil
}

type fileFacts struct {
	device  uint64
	inode   uint64
	size    int64
	mode    uint32
	links   uint64
	modTime int64
}

func artifactFacts(file *os.File) (fileFacts, error) {
	information, err := file.Stat()
	if err != nil {
		return fileFacts{}, fmt.Errorf("inspect release artifact: %w", err)
	}
	stat, ok := information.Sys().(*syscall.Stat_t)
	if !ok || !information.Mode().IsRegular() || information.Mode().Perm() != 0o755 || stat.Nlink != 1 || information.Size() <= 0 || information.Size() > MaxBinaryBytes {
		return fileFacts{}, errors.New("release artifact must be one regular mode-0755 single-link bounded file")
	}
	return fileFacts{
		device: uint64(stat.Dev), inode: uint64(stat.Ino), size: information.Size(),
		mode: uint32(stat.Mode), links: uint64(stat.Nlink), modTime: information.ModTime().UnixNano(),
	}, nil
}

// InspectReleaseArtifact verifies the exact executable bytes and linked
// receipt from one already-opened file. It never reopens a pathname.
func InspectReleaseArtifact(file *os.File, component string, expected Identity) (Identity, error) {
	if !expected.Release() || !validComponent(component) {
		return Identity{}, errors.New("invalid release artifact expectation")
	}
	facts, err := artifactFacts(file)
	if err != nil {
		return Identity{}, err
	}
	mach, err := macho.NewFile(file)
	if err != nil {
		return Identity{}, fmt.Errorf("release artifact is not a thin Mach-O executable: %w", err)
	}
	defer mach.Close()
	wantedCPU := macho.CpuAmd64
	wantedArch := "amd64"
	if expected.target == "darwin/arm64" {
		wantedCPU = macho.CpuArm64
		wantedArch = "arm64"
	}
	if mach.Cpu != wantedCPU || mach.Type != macho.TypeExec {
		return Identity{}, errors.New("release artifact has the wrong Mach-O target")
	}
	goBuild, err := debugbuildinfo.Read(file)
	if err != nil {
		return Identity{}, fmt.Errorf("release artifact has no Go build identity: %w", err)
	}
	if goBuild.Path != "github.com/dark-factory-build/dark-factory/cmd/"+component {
		return Identity{}, errors.New("release artifact has the wrong Go main package")
	}
	settings := buildSettings(goBuild)
	if settings["GOOS"] != "darwin" || settings["GOARCH"] != wantedArch || settings["CGO_ENABLED"] != "0" || settings["-buildmode"] != "exe" || settings["-trimpath"] != "true" {
		return Identity{}, errors.New("release artifact has unsupported Go build settings")
	}
	if _, present := settings["vcs.revision"]; present {
		return Identity{}, errors.New("release artifact unexpectedly carries mutable VCS metadata")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Identity{}, fmt.Errorf("rewind release artifact: %w", err)
	}
	content, err := io.ReadAll(io.NewSectionReader(file, 0, facts.size))
	if err != nil {
		return Identity{}, fmt.Errorf("read release artifact receipt: %w", err)
	}
	linked := []byte(expected.Receipt())
	if len(linked) == 0 || bytes.Count(content, linked) != 1 {
		return Identity{}, errors.New("release artifact does not contain its exact linked receipt once")
	}
	return expected, nil
}

func buildSettings(information *debug.BuildInfo) map[string]string {
	result := make(map[string]string, len(information.Settings))
	for _, setting := range information.Settings {
		if _, exists := result[setting.Key]; exists {
			result[setting.Key] = ""
			continue
		}
		result[setting.Key] = setting.Value
	}
	return result
}

func validComponent(value string) bool {
	return value == "factoryd" || value == "factory-runner" || value == "factoryctl"
}

// ValidateArchiveBounds applies the fixed per-target archive limits.
func ValidateArchiveBounds(unpackedBytes, archiveBytes int64) error {
	if ValidateTargetBounds(unpackedBytes) != nil || archiveBytes <= 0 || archiveBytes > MaxArchiveBytes || unpackedBytes > archiveBytes*MaxCompressionRatio {
		return errors.New("release archive exceeds fixed size or compression bounds")
	}
	return nil
}

// ValidateTargetBounds caps input before tar or gzip can consume it.
func ValidateTargetBounds(unpackedBytes int64) error {
	if unpackedBytes <= 0 || unpackedBytes > MaxTargetBytes {
		return errors.New("release target exceeds the aggregate input bound")
	}
	return nil
}

// ValidateReleaseArchiveBounds caps the complete two-archive publication.
func ValidateReleaseArchiveBounds(armBytes, intelBytes int64) error {
	if armBytes <= 0 || intelBytes <= 0 || armBytes > MaxReleaseArchiveBytes-intelBytes {
		return errors.New("release archives exceed the aggregate bound")
	}
	return nil
}
