package kernel

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openOperationalTestStore(path string) (*Store, error) {
	home, err := os.Open(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	database, err := os.Open(path)
	if err != nil {
		return nil, errors.Join(err, home.Close())
	}
	return OpenOperational(context.Background(), path, home, database)
}

func TestOpenRequiresCanonicalOwnerOnlyDatabaseParent(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("noncanonical path", func(t *testing.T) {
		path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
		writeSidecar(t, path, image, 0o600)
		noncanonical := filepath.Join(filepath.Dir(path), ".", filepath.Base(path))
		if noncanonical == path {
			noncanonical = filepath.Dir(path) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(path)
		}
		if _, err := Open(context.Background(), noncanonical); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("noncanonical path error = %v", err)
		}
	})

	t.Run("symlink ancestor", func(t *testing.T) {
		root := filepath.Dir(mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "placeholder")))
		realParent := filepath.Join(root, "real", "home")
		if err := os.MkdirAll(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(realParent, "kernel.db")
		writeSidecar(t, path, image, 0o600)
		if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked")); err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(root, "linked", "home", "kernel.db")
		if _, err := Open(context.Background(), linked); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("symlink ancestor error = %v", err)
		}
	})

	t.Run("wrong final parent mode", func(t *testing.T) {
		path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
		writeSidecar(t, path, image, 0o600)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), path); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("wrong parent mode error = %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("parent-mode refusal changed database")
		}
	})

	t.Run("final parent is not directory", func(t *testing.T) {
		root := filepath.Dir(mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "placeholder")))
		component := filepath.Join(root, "not-a-directory")
		writeSidecar(t, component, []byte("sentinel"), 0o600)
		if _, err := Open(context.Background(), filepath.Join(component, "kernel.db")); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("non-directory parent error = %v", err)
		}
	})
}

func TestDatabasePathAuthorityDetectsEveryRetainedComponentReplacement(t *testing.T) {
	for _, cut := range []string{"first", "second"} {
		t.Run(cut, func(t *testing.T) {
			root := filepath.Dir(mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "placeholder")))
			first := filepath.Join(root, "first")
			second := filepath.Join(first, "second")
			if err := os.MkdirAll(second, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(second, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(second, "kernel.db")
			authority, err := openDatabasePathAuthority(path)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()

			target := second
			if cut == "first" {
				target = first
			}
			if err := os.Rename(target, target+".old"); err != nil {
				t.Fatal(err)
			}
			if cut == "first" {
				if err := os.MkdirAll(second, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(second, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(second, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := authority.recheck(); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("%s component replacement error = %v", cut, err)
			}
		})
	}

	t.Run("mode changed after pin", func(t *testing.T) {
		path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
		authority, err := openDatabasePathAuthority(path)
		if err != nil {
			t.Fatal(err)
		}
		defer authority.Close()
		if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := authority.recheck(); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("changed parent mode error = %v", err)
		}
	})
}

func TestOpenRefusesDatabaseAndSidecarReplacementDuringPhysicalConnect(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		name := "main"
		if suffix != "" {
			name = suffix[1:]
		}
		t.Run(name, func(t *testing.T) {
			image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
			writeSidecar(t, path, image, 0o600)
			replacement := bytes.Repeat([]byte{byte(len(suffix) + 1)}, 257)
			if suffix == "-shm" {
				replacement = bytes.Repeat([]byte{3}, walIndexRegionSize)
			}
			var replaced bool
			replace := func() error {
				if replaced {
					return nil
				}
				replaced = true
				target := path + suffix
				if err := os.Rename(target, target+".original"); err != nil {
					return err
				}
				return os.WriteFile(target, replacement, 0o600)
			}
			if suffix == "-wal" {
				sqliteActivationHook = func(point string) error {
					if point != "after validation" {
						return nil
					}
					return replace()
				}
			} else {
				sqliteConnectHook = func(kind string, opened int) error {
					if kind != "writer" || opened != 1 {
						return nil
					}
					return replace()
				}
			}
			defer func() {
				sqliteConnectHook = nil
				sqliteActivationHook = nil
			}()
			store, err := openOperationalTestStore(path)
			if store != nil {
				_ = store.Close()
				t.Fatal("Open returned a Store after a retained sqlite file was replaced")
			}
			if !replaced || !errors.Is(err, ErrCorruptState) {
				t.Fatalf("replacement Open error = %v, replaced=%v", err, replaced)
			}
			got, readErr := os.ReadFile(path + suffix)
			if readErr != nil || !bytes.Equal(got, replacement) {
				t.Fatalf("replacement %s changed during refused activation: %x, %v", name, got, readErr)
			}
		})
	}
}

func TestStorePinsFiniteConnectionsAndRefusesReplacementUnderPressure(t *testing.T) {
	root := filepath.Dir(mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "placeholder")))
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "kernel.db")
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	writeSidecar(t, path, image, 0o600)
	store, err := openOperationalTestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if stats := store.writer.Stats(); stats.OpenConnections != 1 || stats.Idle != 1 {
		t.Fatalf("writer pool = %+v, want one pinned idle connection", stats)
	}
	if stats := store.readers.Stats(); stats.OpenConnections != maxReaders || stats.Idle != maxReaders {
		t.Fatalf("reader pool = %+v, want %d pinned idle connections", stats, maxReaders)
	}

	moved := home + ".original"
	if err := os.Rename(home, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	replacements := map[string][]byte{
		"":     []byte("replacement-main"),
		"-wal": []byte("replacement-wal"),
		"-shm": bytes.Repeat([]byte{7}, walIndexRegionSize),
	}
	for suffix, contents := range replacements {
		if err := os.WriteFile(path+suffix, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var group sync.WaitGroup
	errorsSeen := make(chan error, 4*maxReaders)
	for index := 0; index < cap(errorsSeen); index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			_, readErr := store.Factory(context.Background())
			errorsSeen <- readErr
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if !errors.Is(err, ErrCorruptState) {
			t.Fatalf("read after whole-home replacement = %v, want corrupt state", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, want := range replacements {
		got, err := os.ReadFile(path + suffix)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("replacement %q changed under connection pressure: %x, %v", suffix, got, err)
		}
	}
}

func TestStoreRefusesPhysicalReconnectAfterDiscard(t *testing.T) {
	root := filepath.Dir(mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "placeholder")))
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "kernel.db")
	writeSidecar(t, path, image, 0o600)
	store, err := openOperationalTestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connections := make([]*sql.Conn, 0, maxReaders)
	for len(connections) < maxReaders {
		connection, err := store.readerConnection(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	for _, connection := range connections {
		discardConnection(connection)
	}
	if connection, err := store.readers.Conn(context.Background()); connection != nil || !errors.Is(err, errConnectionSetExhausted) {
		if connection != nil {
			_ = connection.Close()
		}
		t.Fatalf("physical reconnect after discarding the finite reader set = %v, %v; want exhausted binding", connection, err)
	}

	if err := os.Rename(home, home+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	replacements := map[string][]byte{
		"":     []byte("replacement-main-after-discard"),
		"-wal": []byte("replacement-wal-after-discard"),
		"-shm": bytes.Repeat([]byte{8}, walIndexRegionSize),
	}
	for suffix, contents := range replacements {
		if err := os.WriteFile(path+suffix, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Factory(context.Background()); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("operation after connection discard and replacement = %v, want corrupt state", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for suffix, want := range replacements {
		got, err := os.ReadFile(path + suffix)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("replacement %q changed after connection discard: %x, %v", suffix, got, err)
		}
	}
}

func TestFailedConfiguredActivationLeavesPairedSidecarEvidence(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
	writeSidecar(t, path, image, 0o600)
	injected := errors.New("injected activation acknowledgement failure")
	sqliteActivationHook = func(point string) error {
		if point != "after validation" {
			t.Fatalf("activation hook point = %q", point)
		}
		return injected
	}
	defer func() { sqliteActivationHook = nil }()
	store, err := openOperationalTestStore(path)
	if store != nil || !errors.Is(err, injected) {
		t.Fatalf("faulted activation = %v, %v", store, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		info, statErr := os.Lstat(path + suffix)
		if statErr != nil || info.Mode() != 0o600 {
			t.Fatalf("faulted activation sidecar %s = %v, %v", suffix, info, statErr)
		}
	}
}
