//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuntimeCreatePublishClosePreservesExactPrivateEffects(t *testing.T) {
	before := openFDCensus(t)
	parentPath := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := openDirectory(t, parentPath)
	runtime, err := CreateRuntime(parent, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Path() != filepath.Join(parentPath, "run-1") || runtime.Identity().Device == 0 || runtime.Identity().Inode == 0 {
		t.Fatalf("runtime = %q %+v", runtime.Path(), runtime.Identity())
	}
	assertDirectory(t, runtime.Path(), runtime.Identity().Device, runtime.Identity().Inode, 0o700)

	duplicate, err := runtime.DuplicateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Fd() == runtime.dir.Fd() {
		t.Fatal("directory descriptor was not duplicated")
	}
	if err := duplicate.Close(); err != nil {
		t.Fatal(err)
	}

	token := [32]byte{1, 2, 3, 4}
	tokenFile, err := runtime.PublishAttemptToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, tokenFile, token[:])
	config := workerConfigFixture()
	encodedConfig, err := encodeWorkerConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	configFile, err := runtime.PublishWorkerConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateFile(t, configFile, encodedConfig)

	if _, err := runtime.PublishAttemptToken(context.Background(), token); !errors.Is(err, errInvalidContract) {
		t.Fatalf("duplicate token error = %v", err)
	}
	if _, err := runtime.PublishWorkerConfig(context.Background(), config); !errors.Is(err, errInvalidContract) {
		t.Fatalf("duplicate config error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second close = %v", err)
	}
	if _, err := os.Stat(runtime.Path()); err != nil {
		t.Fatalf("Close removed runtime: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	assertFDCensus(t, before)
}

func TestRuntimeRejectsExistingAndUnsafeAuthoritiesWithoutMutation(t *testing.T) {
	tests := map[string]func(string) error{
		"directory": func(path string) error { return os.Mkdir(path, 0o700) },
		"file":      func(path string) error { return os.WriteFile(path, []byte("sentinel"), 0o600) },
		"symlink":   func(path string) error { return os.Symlink("missing", path) },
		"fifo":      func(path string) error { return unix.Mkfifo(path, 0o600) },
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			parentPath := filepath.Join(t.TempDir(), "private")
			if err := os.Mkdir(parentPath, 0o700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parentPath, "run")
			if err := create(target); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(target)
			if err != nil {
				t.Fatal(err)
			}
			parent := openDirectory(t, parentPath)
			defer parent.Close()
			if _, err := CreateRuntime(parent, "run"); !errors.Is(err, errInvalidContract) {
				t.Fatalf("CreateRuntime error = %v", err)
			}
			after, err := os.Lstat(target)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing authority changed: %v", err)
			}
			if name == "file" {
				contents, _ := os.ReadFile(target)
				if string(contents) != "sentinel" {
					t.Fatalf("existing file replaced: %q", contents)
				}
			}
		})
	}

	parentPath := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(parentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := openDirectory(t, parentPath)
	defer parent.Close()
	if _, err := CreateRuntime(parent, "run"); !errors.Is(err, errInvalidContract) {
		t.Fatalf("shared parent error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(parentPath, "run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared parent gained effect: %v", err)
	}
}

func TestRuntimeParentAndLeafSwapsFailClosed(t *testing.T) {
	t.Run("parent", func(t *testing.T) {
		root := t.TempDir()
		parentPath := filepath.Join(root, "private")
		if err := os.Mkdir(parentPath, 0o700); err != nil {
			t.Fatal(err)
		}
		parent := openDirectory(t, parentPath)
		defer parent.Close()
		_, err := createRuntime(parent, "run", func() {
			if err := os.Rename(parentPath, parentPath+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(parentPath, 0o700); err != nil {
				t.Fatal(err)
			}
		}, nil)
		if !errors.Is(err, errInvalidContract) {
			t.Fatalf("parent swap error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(parentPath, "run")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("replacement parent gained runtime: %v", err)
		}
	})

	t.Run("leaf", func(t *testing.T) {
		parentPath := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(parentPath, 0o700); err != nil {
			t.Fatal(err)
		}
		parent := openDirectory(t, parentPath)
		defer parent.Close()
		_, err := createRuntime(parent, "run", nil, func() {
			if err := os.Rename(filepath.Join(parentPath, "run"), filepath.Join(parentPath, "old")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(parentPath, "run"), 0o700); err != nil {
				t.Fatal(err)
			}
		})
		if !errors.Is(err, errInvalidContract) {
			t.Fatalf("leaf swap error = %v", err)
		}
	})
}

func TestPrivatePublicationRejectsSpecialExistingAndCleansPartialWrite(t *testing.T) {
	invalidRuntime := newTestRuntime(t)
	defer invalidRuntime.Close()
	invalidConfig := workerConfigFixture()
	invalidConfig.RepositoryRoot = "relative-private-sentinel"
	if _, err := invalidRuntime.PublishWorkerConfig(context.Background(), invalidConfig); !errors.Is(err, errInvalidContract) {
		t.Fatalf("invalid config error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(invalidRuntime.Path(), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid config gained file effect: %v", err)
	}

	for name, create := range map[string]func(string) error{
		"symlink": func(path string) error { return os.Symlink("missing", path) },
		"fifo":    func(path string) error { return unix.Mkfifo(path, 0o600) },
		"dir":     func(path string) error { return os.Mkdir(path, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			runtime := newTestRuntime(t)
			defer runtime.Close()
			path := filepath.Join(runtime.Path(), workerConfigName)
			if err := create(path); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(path)
			if _, err := runtime.PublishWorkerConfig(context.Background(), workerConfigFixture()); !errors.Is(err, errInvalidContract) {
				t.Fatalf("publish error = %v", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("existing leaf mutated: %v", err)
			}
		})
	}

	runtime := newTestRuntime(t)
	defer runtime.Close()
	writes := 0
	_, err := runtime.publish(context.Background(), workerConfigName, []byte("partial-write"), workerConfigLimit, func(fd int, value []byte) (int, error) {
		writes++
		if writes == 1 {
			return unix.Write(fd, value[:3])
		}
		return 0, syscall.EIO
	})
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("partial write error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(runtime.Path(), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file retained: %v", err)
	}

	leafRuntime := newTestRuntime(t)
	defer leafRuntime.Close()
	leafPath := filepath.Join(leafRuntime.Path(), workerConfigName)
	_, err = leafRuntime.publish(context.Background(), workerConfigName, []byte("private"), workerConfigLimit, func(fd int, value []byte) (int, error) {
		if err := os.Rename(leafPath, leafPath+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(leafPath, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		return unix.Write(fd, value)
	})
	if !errors.Is(err, errInvalidContract) {
		t.Fatalf("leaf swap error = %v", err)
	}
	replacement, readErr := os.ReadFile(leafPath)
	if readErr != nil || string(replacement) != "replacement" {
		t.Fatalf("leaf replacement mutated = %q, %v", replacement, readErr)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.PublishWorkerConfig(canceled, workerConfigFixture()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(runtime.Path(), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled publish gained effect: %v", err)
	}

	midRuntime := newTestRuntime(t)
	defer midRuntime.Close()
	midContext, midCancel := context.WithCancel(context.Background())
	_, err = midRuntime.publish(midContext, workerConfigName, []byte("cancel-between-writes"), workerConfigLimit, func(fd int, value []byte) (int, error) {
		written, writeErr := unix.Write(fd, value[:1])
		midCancel()
		return written, writeErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-write cancellation error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(midRuntime.Path(), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mid-write cancellation retained file: %v", err)
	}
}

func TestRuntimeValuesAndErrorsRedactPrivateSentinels(t *testing.T) {
	runtime := newTestRuntime(t)
	defer runtime.Close()
	config := workerConfigFixture()
	config.StartupInput = []byte("PRIVATE-CONTENTS")
	file, err := runtime.PublishWorkerConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{fmt.Sprint(file), fmt.Sprintf("%#v", file)} {
		if stringsContainsAny(formatted, runtime.Path(), "PRIVATE-CONTENTS") {
			t.Fatalf("private file formatting leaked: %q", formatted)
		}
	}
	config.StartupInput = []byte("SECOND-PRIVATE")
	_, err = runtime.PublishWorkerConfig(context.Background(), config)
	if err == nil || stringsContainsAny(err.Error(), runtime.Path(), "SECOND-PRIVATE") {
		t.Fatalf("private error leaked: %v", err)
	}
}

func newTestRuntime(t testing.TB) *Runtime {
	t.Helper()
	parentPath := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := openDirectory(t, parentPath)
	runtime, err := CreateRuntime(parent, "run")
	if closeErr := parent.Close(); err != nil || closeErr != nil {
		t.Fatalf("CreateRuntime = %v, close = %v", err, closeErr)
	}
	return runtime
}

func openDirectory(t testing.TB, path string) *os.File {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		t.Fatal(err)
	}
	return os.NewFile(uintptr(fd), path)
}

func assertDirectory(t testing.TB, path string, device, inode uint64, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != mode || uint64(stat.Dev) != device || stat.Ino != inode || stat.Uid != uint32(os.Geteuid()) {
		t.Fatalf("unsafe directory: %+v", info)
	}
}

func assertPrivateFile(t testing.TB, file PrivateFile, want []byte) {
	t.Helper()
	got, err := os.ReadFile(file.Path())
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("private contents = %x, %v", got, err)
	}
	info, err := os.Lstat(file.Path())
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || uint64(stat.Dev) != file.Identity().Device || stat.Ino != file.Identity().Inode {
		t.Fatalf("unsafe private file: %+v", info)
	}
}

func openFDCensus(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func assertFDCensus(t testing.TB, before int) {
	t.Helper()
	after := openFDCensus(t)
	if after != before {
		t.Fatalf("FD census before=%d after=%d", before, after)
	}
}

func stringsContainsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if candidate != "" && bytes.Contains([]byte(value), []byte(candidate)) {
			return true
		}
	}
	return false
}
