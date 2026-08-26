package kernel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
)

func TestCreateOpenExactSchemaAndIdentity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kernel.db")
	at := mustTime(t, 42)
	store, err := Create(ctx, path, FactoryConfig{}, at)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	state, err := store.Factory(ctx)
	if err != nil {
		t.Fatalf("Factory: %v", err)
	}
	if state.DispatchEnabled || state.Capacity != 1 || state.Revision.Int64() != 1 || state.Head.Int64() != 0 || state.Floor.Int64() != 1 {
		t.Fatalf("unexpected initial factory: %+v", state)
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	appID, version, err := inspectIdentity(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	var objects int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	if appID != applicationID || version != userVersion || objects != len(schemaStatements) {
		t.Fatalf("identity/schema = %#x/%d/%d, want %#x/%d/%d", appID, version, objects, applicationID, userVersion, len(schemaStatements))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("database mode = %#o, want 0600", got)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, path, FactoryConfig{}, at); err == nil {
		t.Fatal("Create accepted an existing database")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused Create modified the existing database")
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAndOpenRejectForeignPathsWithoutModification(t *testing.T) {
	ctx := context.Background()
	at := mustTime(t, 1)
	if _, err := Create(ctx, "relative.db", FactoryConfig{}, at); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("relative Create error = %v", err)
	}

	t.Run("empty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.db")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		before := captureDatabaseEvidence(t, path)
		if _, err := Open(ctx, path); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("Open error = %v", err)
		}
		assertDatabaseEvidenceUnchanged(t, path, before)
	})

	t.Run("foreign application", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "rust.db")
		raw := openRaw(t, path)
		if _, err := raw.Exec(`PRAGMA application_id = 1146242898`); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`PRAGMA user_version = 19`); err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`CREATE TABLE rust_state(id INTEGER PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		raw.Close()
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		before := captureDatabaseEvidence(t, path)
		if _, err := Open(ctx, path); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("Open error = %v", err)
		}
		assertDatabaseEvidenceUnchanged(t, path, before)
		if _, err := os.Stat(path + "-wal"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("foreign Open created WAL: %v", err)
		}
	})

	t.Run("wrong mode", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mode.db")
		store, err := Create(ctx, path, FactoryConfig{}, at)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		before := captureDatabaseEvidence(t, path)
		if _, err := Open(ctx, path); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("Open error = %v", err)
		}
		assertDatabaseEvidenceUnchanged(t, path, before)
	})

	t.Run("symlink", func(t *testing.T) {
		directory := t.TempDir()
		target := filepath.Join(directory, "target.db")
		store, err := Create(ctx, target, FactoryConfig{}, at)
		if err != nil {
			t.Fatal(err)
		}
		store.Close()
		link := filepath.Join(directory, "link.db")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, link); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("Open error = %v", err)
		}
	})
}

func TestOpenRejectsUnknownVersionAndPartialIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *sql.DB){
		"unknown version": func(t *testing.T, raw *sql.DB) {
			if _, err := raw.Exec(`PRAGMA user_version = 2`); err != nil {
				t.Fatal(err)
			}
		},
		"partial schema": func(t *testing.T, raw *sql.DB) {
			if _, err := raw.Exec(`DROP TABLE resources`); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kernel.db")
			store, err := Create(context.Background(), path, FactoryConfig{}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			raw := openRaw(t, path)
			mutate(t, raw)
			raw.Close()
			before := captureDatabaseEvidence(t, path)
			if _, err := Open(context.Background(), path); !errors.Is(err, ErrForeignDatabase) {
				t.Fatalf("Open error = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, before)
		})
	}
}

func TestOpenRejectsEverySchemaDrift(t *testing.T) {
	tests := map[string]string{
		"missing": `DROP INDEX tasks_canonical_queue`,
		"extra":   `CREATE TABLE surprise(value INTEGER) STRICT`,
		"changed": `DROP INDEX projects_root_unique; CREATE UNIQUE INDEX projects_root_unique ON projects(root, name)`,
	}
	for name, mutation := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kernel.db")
			store, err := Create(context.Background(), path, FactoryConfig{}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			raw := openRaw(t, path)
			if _, err := raw.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			before := captureDatabaseEvidence(t, path)
			if _, err := Open(context.Background(), path); !errors.Is(err, ErrForeignDatabase) {
				t.Fatalf("Open error = %v", err)
			}
			assertDatabaseEvidenceUnchanged(t, path, before)
		})
	}
}

func TestConnectionsVerifyExactPolicyAndDiscardPoison(t *testing.T) {
	store, path := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	for _, pool := range []*sql.DB{store.writer, store.readers} {
		for name, poison := range map[string]string{
			"foreign_keys": `PRAGMA foreign_keys = OFF`,
			"busy_timeout": `PRAGMA busy_timeout = 7`,
			"synchronous":  `PRAGMA synchronous = OFF`,
		} {
			t.Run(name, func(t *testing.T) {
				connection, err := pool.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := connection.ExecContext(ctx, poison); err != nil {
					t.Fatal(err)
				}
				connection.Close()
				var checkout func(context.Context) (*sql.Conn, error)
				if pool == store.writer {
					checkout = store.writerConnection
				} else {
					checkout = store.readerConnection
				}
				if connection, err := checkout(ctx); err == nil {
					connection.Close()
					t.Fatal("poisoned connection passed verification")
				}
				replacement, err := checkout(ctx)
				if err != nil {
					t.Fatalf("verified replacement: %v", err)
				}
				replacement.Close()
			})
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestLiteralImmediateExclusionAndCancelledWait(t *testing.T) {
	store, path := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	other := openConfiguredRaw(t, path)
	defer other.Close()
	if _, err := other.Exec(`PRAGMA busy_timeout = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`BEGIN IMMEDIATE`); err == nil {
		_, _ = other.Exec(`ROLLBACK`)
		t.Fatal("second immediate writer acquired the write reservation")
	} else if !errors.Is(err, sqlite3.BUSY) && !errors.Is(err, sqlite3.LOCKED) {
		t.Fatalf("second immediate writer error = %v", err)
	}
	if err := tx.Rollback(nil); err != nil {
		t.Fatal(err)
	}
	tx.Close()

	if _, err := other.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	project := NewProject{ID: projectID(t, 9), Name: "blocked", Root: filepath.Join(t.TempDir(), "root")}
	_, err = store.CreateProject(deadline, project, mustTime(t, 5))
	var unknown *OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled writer error = %v", err)
	}
	if _, err := other.Exec(`ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Project(ctx, project.ID); err != nil || found {
		t.Fatalf("blocked body footprint found=%v err=%v", found, err)
	}
	if _, err := store.CreateProject(ctx, project, mustTime(t, 5)); err != nil {
		t.Fatalf("replacement writer failed: %v", err)
	}
}

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kernel.db")
	store, err := Create(context.Background(), path, FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return store, path
}

func mustTime(t *testing.T, value int64) UnixMillis {
	t.Helper()
	result, err := NewUnixMillis(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func projectID(t *testing.T, seed byte) ProjectID {
	t.Helper()
	result, err := ProjectIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func agentID(t *testing.T, seed byte) AgentID {
	t.Helper()
	result, err := AgentIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func taskID(t *testing.T, seed byte) TaskID {
	t.Helper()
	result, err := TaskIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func incarnationID(t *testing.T, seed byte) IncarnationID {
	t.Helper()
	result, err := IncarnationIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func runID(t *testing.T, seed byte) RunID {
	t.Helper()
	result, err := RunIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func changeID(t *testing.T, seed byte) ChangeID {
	t.Helper()
	result, err := ChangeIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func resourceID(t *testing.T, seed byte) ResourceID {
	t.Helper()
	result, err := ResourceIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func terminalSessionID(t *testing.T, seed byte) TerminalSessionID {
	t.Helper()
	value, err := TerminalSessionIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	pool, err := sql.Open(driverName, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}

func openConfiguredRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	pool, err := sql.Open(driverName, configuredDataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Ping(); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool
}

func TestCloseDoesNotRetainKernelGoroutines(t *testing.T) {
	baseline := runtime.NumGoroutine()
	for range 10 {
		store, _ := newTestStore(t)
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if delta := runtime.NumGoroutine() - baseline; delta > 2 {
		t.Fatalf("kernel stores retained %d goroutines", delta)
	}
}

type databaseEvidence struct {
	hash       [sha256.Size]byte
	info       os.FileInfo
	dirEntries []string
}

func captureDatabaseEvidence(t *testing.T, path string) databaseEvidence {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name()+":"+entry.Type().String())
	}
	return databaseEvidence{hash: sha256.Sum256(contents), info: info, dirEntries: names}
}

func assertDatabaseEvidenceUnchanged(t *testing.T, path string, before databaseEvidence) {
	t.Helper()
	after := captureDatabaseEvidence(t, path)
	if after.hash != before.hash || !os.SameFile(before.info, after.info) || after.info.Size() != before.info.Size() || after.info.Mode() != before.info.Mode() || !after.info.ModTime().Equal(before.info.ModTime()) || !equalStrings(after.dirEntries, before.dirEntries) {
		t.Fatalf("database refusal changed filesystem evidence:\nbefore hash=%x size=%d mode=%v mtime=%v entries=%v\nafter  hash=%x size=%d mode=%v mtime=%v entries=%v",
			before.hash, before.info.Size(), before.info.Mode(), before.info.ModTime(), before.dirEntries,
			after.hash, after.info.Size(), after.info.Mode(), after.info.ModTime(), after.dirEntries)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
