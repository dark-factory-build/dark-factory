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
	runtimePath := mustRuntimePath(t, runtime)
	if runtimePath != filepath.Join(parentPath, "run-1") || runtime.Identity().Device == 0 || runtime.Identity().Inode == 0 {
		t.Fatalf("runtime = %q %+v", runtimePath, runtime.Identity())
	}
	assertDirectory(t, runtimePath, runtime.Identity().Device, runtime.Identity().Inode, 0o700)

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
	config := workerConfigForRuntime(t, runtime)
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
	if _, err := runtime.Path(); !errors.Is(err, errInvalidContract) {
		t.Fatalf("closed runtime path error = %v", err)
	}
	if _, err := os.Stat(runtimePath); err != nil {
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
		}, nil, nil)
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
		}, nil)
		if !errors.Is(err, errInvalidContract) {
			t.Fatalf("leaf swap error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(parentPath, "old")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("exact moved runtime was not cleaned: %v", err)
		}
		if info, err := os.Lstat(filepath.Join(parentPath, "run")); err != nil || !info.IsDir() {
			t.Fatalf("foreign runtime replacement changed: %v %v", info, err)
		}
	})
}

func TestRuntimeAuthorityRejectsPathReplacementAtEveryAccessor(t *testing.T) {
	t.Run("before publication", func(t *testing.T) {
		runtime := newTestRuntime(t)
		defer runtime.Close()
		config := workerConfigForRuntime(t, runtime)
		path := mustRuntimePath(t, runtime)
		moved := path + ".moved"
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.Path(); !errors.Is(err, errInvalidContract) {
			t.Fatalf("Path after replacement = %v", err)
		}
		if duplicate, err := runtime.DuplicateDirectory(); !errors.Is(err, errInvalidContract) || duplicate != nil {
			t.Fatalf("DuplicateDirectory after replacement = %v, %v", duplicate, err)
		}
		if _, err := runtime.PublishAttemptToken(context.Background(), [32]byte{1}); !errors.Is(err, errInvalidContract) {
			t.Fatalf("token after replacement = %v", err)
		}
		if _, err := runtime.PublishWorkerConfig(context.Background(), config); !errors.Is(err, errInvalidContract) {
			t.Fatalf("config after replacement = %v", err)
		}
		for _, root := range []string{path, moved} {
			for _, name := range []string{attemptTokenName, workerConfigName} {
				if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("replaced runtime gained %s under %s: %v", name, root, err)
				}
			}
		}
	})

	t.Run("after publication", func(t *testing.T) {
		runtime := newTestRuntime(t)
		defer runtime.Close()
		file, err := runtime.PublishAttemptToken(context.Background(), [32]byte{2})
		if err != nil {
			t.Fatal(err)
		}
		path := mustRuntimePath(t, runtime)
		moved := path + ".moved"
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Path(); !errors.Is(err, errInvalidContract) {
			t.Fatalf("private file path after runtime replacement = %v", err)
		}
		if _, err := runtime.Path(); !errors.Is(err, errInvalidContract) {
			t.Fatalf("runtime path after publication replacement = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(path, attemptTokenName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("foreign replacement gained token: %v", err)
		}
	})
}

func TestFailedRuntimeCreationCleansOnlyExactEmptyIdentity(t *testing.T) {
	t.Run("fsync failure", func(t *testing.T) {
		parentPath := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(parentPath, 0o700); err != nil {
			t.Fatal(err)
		}
		parent := openDirectory(t, parentPath)
		defer parent.Close()
		calls := 0
		syncDirectory := func(fd int) error {
			calls++
			if calls == 1 {
				return syscall.EIO
			}
			return unix.Fsync(fd)
		}
		if _, err := createRuntime(parent, "run", nil, nil, syncDirectory); !errors.Is(err, syscall.EIO) || errors.Is(err, errRetainedRuntime) {
			t.Fatalf("fsync failure = %v", err)
		}
		if calls < 2 {
			t.Fatalf("cleanup did not fsync parent: calls=%d", calls)
		}
		if _, err := os.Lstat(filepath.Join(parentPath, "run")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed runtime retained after exact cleanup: %v", err)
		}
	})

	t.Run("nonempty retained uncertainty", func(t *testing.T) {
		parentPath := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(parentPath, 0o700); err != nil {
			t.Fatal(err)
		}
		parent := openDirectory(t, parentPath)
		defer parent.Close()
		_, err := createRuntime(parent, "run", nil, func() {
			if err := os.Rename(filepath.Join(parentPath, "run"), filepath.Join(parentPath, "retained")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(parentPath, "retained", "foreign"), []byte("sentinel"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(parentPath, "run"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, nil)
		if !errors.Is(err, errRetainedRuntime) {
			t.Fatalf("nonempty retained failure = %T %v", err, err)
		}
		contents, readErr := os.ReadFile(filepath.Join(parentPath, "retained", "foreign"))
		if readErr != nil || string(contents) != "sentinel" {
			t.Fatalf("retained uncertainty was deleted: %q %v", contents, readErr)
		}
		if info, statErr := os.Lstat(filepath.Join(parentPath, "run")); statErr != nil || !info.IsDir() {
			t.Fatalf("foreign replacement changed: %v %v", info, statErr)
		}
	})

	t.Run("bounded search refusal", func(t *testing.T) {
		parentPath := filepath.Join(t.TempDir(), "private")
		if err := os.Mkdir(parentPath, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := 0; index <= cleanupSearchLimit; index++ {
			if err := os.Mkdir(filepath.Join(parentPath, fmt.Sprintf("filler-%03d", index)), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		parent := openDirectory(t, parentPath)
		defer parent.Close()
		_, err := createRuntime(parent, "run", nil, func() {
			if err := os.Rename(filepath.Join(parentPath, "run"), filepath.Join(parentPath, "retained")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(parentPath, "run"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, nil)
		if !errors.Is(err, errRetainedRuntime) {
			t.Fatalf("bounded search failure = %v", err)
		}
		for _, name := range []string{"run", "retained"} {
			if info, statErr := os.Lstat(filepath.Join(parentPath, name)); statErr != nil || !info.IsDir() {
				t.Fatalf("bounded refusal deleted %s: %v %v", name, info, statErr)
			}
		}
	})
}

func TestPrivatePublicationRejectsSpecialExistingAndCleansPartialWrite(t *testing.T) {
	invalidRuntime := newTestRuntime(t)
	defer invalidRuntime.Close()
	invalidConfig := workerConfigForRuntime(t, invalidRuntime)
	invalidConfig.RepositoryRoot = "relative-private-sentinel"
	if _, err := invalidRuntime.PublishWorkerConfig(context.Background(), invalidConfig); !errors.Is(err, errInvalidContract) {
		t.Fatalf("invalid config error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(mustRuntimePath(t, invalidRuntime), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
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
			path := filepath.Join(mustRuntimePath(t, runtime), workerConfigName)
			if err := create(path); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(path)
			if _, err := runtime.PublishWorkerConfig(context.Background(), workerConfigForRuntime(t, runtime)); !errors.Is(err, errInvalidContract) {
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
	}, nil)
	if !errors.Is(err, syscall.EIO) {
		t.Fatalf("partial write error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(mustRuntimePath(t, runtime), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file retained: %v", err)
	}

	leafRuntime := newTestRuntime(t)
	defer leafRuntime.Close()
	leafPath := filepath.Join(mustRuntimePath(t, leafRuntime), workerConfigName)
	_, err = leafRuntime.publish(context.Background(), workerConfigName, []byte("private"), workerConfigLimit, func(fd int, value []byte) (int, error) {
		if err := os.Rename(leafPath, leafPath+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(leafPath, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		return unix.Write(fd, value)
	}, nil)
	if !errors.Is(err, errInvalidContract) {
		t.Fatalf("leaf swap error = %v", err)
	}
	replacement, readErr := os.ReadFile(leafPath)
	if readErr != nil || string(replacement) != "replacement" {
		t.Fatalf("leaf replacement mutated = %q, %v", replacement, readErr)
	}
	if _, err := os.Lstat(leafPath + ".old"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exact moved private file was not cleaned: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.PublishWorkerConfig(canceled, workerConfigForRuntime(t, runtime)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(mustRuntimePath(t, runtime), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled publish gained effect: %v", err)
	}

	midRuntime := newTestRuntime(t)
	defer midRuntime.Close()
	midContext, midCancel := context.WithCancel(context.Background())
	_, err = midRuntime.publish(midContext, workerConfigName, []byte("cancel-between-writes"), workerConfigLimit, func(fd int, value []byte) (int, error) {
		written, writeErr := unix.Write(fd, value[:1])
		midCancel()
		return written, writeErr
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-write cancellation error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(mustRuntimePath(t, midRuntime), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mid-write cancellation retained file: %v", err)
	}
}

func TestFailedPrivatePublicationCleansThroughRetainedDirectoryAuthority(t *testing.T) {
	t.Run("runtime and leaf replaced", func(t *testing.T) {
		runtime := newTestRuntime(t)
		defer runtime.Close()
		path := mustRuntimePath(t, runtime)
		moved := path + ".moved"
		_, err := runtime.publish(context.Background(), workerConfigName, []byte("private"), workerConfigLimit, func(fd int, value []byte) (int, error) {
			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			return unix.Write(fd, value)
		}, nil)
		if !errors.Is(err, errInvalidContract) || errors.Is(err, errRetainedRuntime) {
			t.Fatalf("runtime replacement publication error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(moved, workerConfigName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("created file was not removed through retained dirfd: %v", err)
		}
		if entries, readErr := os.ReadDir(path); readErr != nil || len(entries) != 0 {
			t.Fatalf("foreign runtime replacement changed: %v %v", entries, readErr)
		}
	})

	t.Run("file fsync failure", func(t *testing.T) {
		runtime := newTestRuntime(t)
		defer runtime.Close()
		calls := 0
		syncDirectory := func(fd int) error {
			calls++
			if calls == 1 {
				return syscall.EIO
			}
			return unix.Fsync(fd)
		}
		_, err := runtime.publish(context.Background(), workerConfigName, []byte("private"), workerConfigLimit, nil, syncDirectory)
		if !errors.Is(err, syscall.EIO) || errors.Is(err, errRetainedRuntime) {
			t.Fatalf("file fsync failure = %v", err)
		}
		if calls < 2 {
			t.Fatalf("file cleanup did not fsync containing directory: calls=%d", calls)
		}
		if _, err := os.Lstat(filepath.Join(mustRuntimePath(t, runtime), workerConfigName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed private file retained after exact cleanup: %v", err)
		}
	})

	t.Run("hardlink retained uncertainty", func(t *testing.T) {
		runtime := newTestRuntime(t)
		defer runtime.Close()
		path := filepath.Join(mustRuntimePath(t, runtime), workerConfigName)
		link := path + ".link"
		_, err := runtime.publish(context.Background(), workerConfigName, []byte("private"), workerConfigLimit, func(fd int, value []byte) (int, error) {
			if err := os.Link(path, link); err != nil {
				t.Fatal(err)
			}
			return unix.Write(fd, value)
		}, nil)
		if !errors.Is(err, errRetainedRuntime) {
			t.Fatalf("hardlink uncertainty = %T %v", err, err)
		}
		for _, retained := range []string{path, link} {
			if info, statErr := os.Lstat(retained); statErr != nil || !info.Mode().IsRegular() {
				t.Fatalf("uncertain hardlink deleted %s: %v %v", retained, info, statErr)
			}
		}
	})
}

func TestRuntimeValuesAndErrorsRedactPrivateSentinels(t *testing.T) {
	runtime := newTestRuntime(t)
	defer runtime.Close()
	config := workerConfigForRuntime(t, runtime)
	config.StartupInput = []byte("PRIVATE-CONTENTS")
	file, err := runtime.PublishWorkerConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{fmt.Sprint(file), fmt.Sprintf("%#v", file)} {
		if stringsContainsAny(formatted, mustRuntimePath(t, runtime), "PRIVATE-CONTENTS") {
			t.Fatalf("private file formatting leaked: %q", formatted)
		}
	}
	config.StartupInput = []byte("SECOND-PRIVATE")
	_, err = runtime.PublishWorkerConfig(context.Background(), config)
	if err == nil || stringsContainsAny(err.Error(), mustRuntimePath(t, runtime), "SECOND-PRIVATE") {
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

func workerConfigForRuntime(t testing.TB, runtime *Runtime) workerConfig {
	t.Helper()
	path := mustRuntimePath(t, runtime)
	config := workerConfigFixture()
	config.RuntimePath = path
	config.RuntimeIdentity = runtime.Identity()
	config.ProviderHome = filepath.Join(path, "home")
	config.ProviderTemp = filepath.Join(path, "tmp")
	config.AttemptTokenPath = filepath.Join(path, attemptTokenName)
	return config
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
	path := mustPrivatePath(t, file)
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("private contents = %x, %v", got, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || uint64(stat.Dev) != file.Identity().Device || stat.Ino != file.Identity().Inode {
		t.Fatalf("unsafe private file: %+v", info)
	}
}

func mustRuntimePath(t testing.TB, runtime *Runtime) string {
	t.Helper()
	path, err := runtime.Path()
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func mustPrivatePath(t testing.TB, file PrivateFile) string {
	t.Helper()
	path, err := file.Path()
	if err != nil {
		t.Fatal(err)
	}
	return path
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
