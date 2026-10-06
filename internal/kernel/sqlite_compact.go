package kernel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	_ "github.com/ncruces/go-sqlite3/driver"
	"io"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the user_version this build opens and writes.
const SchemaVersion = userVersion

// BackupTo writes a consistent copy of the database (VACUUM INTO) to path,
// holding the writer gate so nothing commits during the copy. It never
// removes anything: VACUUM INTO refuses an existing file, so no destination,
// however aliased, can replace a live database file.
func (store *Store) BackupTo(ctx context.Context, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: backup destination already exists", ErrConflict)
	}
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	if err := tx.Rollback(nil); err != nil {
		return err
	}
	if _, err := tx.connection.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// BackupManifest describes the non-database material required to operate a backup.
type BackupManifest struct {
	SchemaVersion         int      `json:"schema_version"`
	SHA256                string   `json:"sha256"`
	CreatedAt             string   `json:"created_at"`
	ExternalPrerequisites []string `json:"external_prerequisites"`
}

func BackupManifestPath(path string) string { return path + ".manifest.json" }

func WriteBackupManifest(path string, created time.Time) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	m := BackupManifest{SchemaVersion: SchemaVersion, SHA256: hex.EncodeToString(h.Sum(nil)), CreatedAt: created.UTC().Format(time.RFC3339Nano), ExternalPrerequisites: []string{"provider logins", "maintainer.json", "account homes"}}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := BackupManifestPath(path) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, BackupManifestPath(path)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func VerifyBackup(ctx context.Context, path string) (BackupManifest, error) {
	data, err := os.ReadFile(BackupManifestPath(path))
	if err != nil {
		return BackupManifest{}, err
	}
	var m BackupManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.SchemaVersion != SchemaVersion || len(m.ExternalPrerequisites) != 3 {
		return m, fmt.Errorf("backup manifest schema mismatch")
	}
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return m, err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return m, fmt.Errorf("backup checksum mismatch")
	}
	db, err := sql.Open(driverName, operationalInspectDataSource(filepath.Clean(path)))
	if err != nil {
		return m, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return m, err
	}
	defer conn.Close()
	if err := validateDatabaseSnapshot(ctx, conn); err != nil {
		return m, err
	}
	return m, nil
}

// CompactStorage is explicit maintenance: retain the writer gate across VACUUM
// (which cannot run inside a transaction), so dispatch cannot restart mid-copy.
func (store *Store) CompactStorage(ctx context.Context) (FactoryState, error) {
	tx, err := store.beginValidatedWrite(ctx)
	if err != nil {
		return FactoryState{}, err
	}
	defer tx.Close()
	state, err := factoryState(ctx, tx.connection)
	if err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	var live bool
	if err := tx.connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM runs WHERE phase <> 'terminal')`).Scan(&live); err != nil {
		return FactoryState{}, tx.Rollback(err)
	}
	if state.DispatchEnabled || live {
		return FactoryState{}, tx.Rollback(fmt.Errorf("%w: compaction requires dispatch off and no nonterminal runs", ErrConflict))
	}
	if err := tx.Rollback(nil); err != nil {
		return FactoryState{}, err
	}
	if _, err := tx.connection.ExecContext(ctx, `VACUUM`); err != nil {
		return FactoryState{}, err
	}
	var busy, pages, checkpointed int
	if err := tx.connection.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &pages, &checkpointed); err != nil {
		return FactoryState{}, err
	}
	if busy != 0 {
		return FactoryState{}, ErrBusy
	}
	if err := validateExactSchema(ctx, tx.connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		return FactoryState{}, err
	}
	return state, nil
}
