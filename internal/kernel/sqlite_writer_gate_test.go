package kernel

import (
	"bytes"
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

func TestWriterAdmissionHonorsContextBeforeSQL(t *testing.T) {
	store, _ := newTestStore(t)
	defer store.Close()

	tx, err := store.beginValidatedWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	project := NewProject{ID: projectID(t, 241), Name: "deadline", Root: filepath.Join(t.TempDir(), "root")}
	at := mustTime(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, writeErr := store.CreateProject(ctx, project, at)
		result <- writeErr
	}()
	select {
	case writeErr := <-result:
		if !errors.Is(writeErr, context.DeadlineExceeded) {
			t.Fatalf("blocked writer error = %v, want deadline exceeded", writeErr)
		}
	case <-time.After(500 * time.Millisecond):
		_ = tx.Rollback(nil)
		tx.Close()
		t.Fatal("blocked writer ignored context while writer gate was held")
	}
	if _, found, err := store.Project(context.Background(), project.ID); err != nil || found {
		t.Fatalf("deadline writer changed durable state: found=%v err=%v", found, err)
	}
	if err := tx.Rollback(nil); err != nil {
		t.Fatal(err)
	}
	tx.Close()
	if _, err := store.CreateProject(context.Background(), project, mustTime(t, 3)); err != nil {
		t.Fatalf("writer after gate release = %v", err)
	}
}

func TestStoreCloseJoinsAdmittedWriterAndRejectsNewWriters(t *testing.T) {
	store, _ := newTestStore(t)
	tx, err := store.beginValidatedWrite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	closeResult := make(chan error, 1)
	go func() { closeResult <- store.Close() }()
	deadline := time.After(500 * time.Millisecond)
	for !store.closed.Load() {
		select {
		case <-deadline:
			t.Fatal("Store.Close did not mark the store closed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case closeErr := <-closeResult:
		t.Fatalf("Store.Close returned with admitted writer: %v", closeErr)
	case <-time.After(25 * time.Millisecond):
	}
	project := NewProject{ID: projectID(t, 242), Name: "closed", Root: filepath.Join(t.TempDir(), "root")}
	at := mustTime(t, 2)
	writeResult := make(chan error, 1)
	go func() {
		_, writeErr := store.CreateProject(context.Background(), project, at)
		writeResult <- writeErr
	}()
	if err := tx.Rollback(nil); err != nil {
		t.Fatal(err)
	}
	tx.Close()
	if closeErr := <-closeResult; closeErr != nil {
		t.Fatalf("Store.Close = %v", closeErr)
	}
	if writeErr := <-writeResult; !errors.Is(writeErr, ErrStoreClosed) {
		t.Fatalf("writer admitted after Store.Close = %v, want store closed", writeErr)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("repeated Store.Close = %v", err)
	}
}

func TestStoreCloseRetainsAuthorityForCheckedOutConnection(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
	writeSidecar(t, path, image, 0o600)
	store, err := openOperationalTestStore(path)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.readerConnection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- store.Close() }()
	deadline := time.After(500 * time.Millisecond)
	for !store.closed.Load() {
		select {
		case <-deadline:
			t.Fatal("Store.Close did not mark the store closed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case err := <-closeResult:
		t.Fatalf("Store.Close returned while a checked-out connection was idle: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if store.pathBinding == nil {
		t.Fatal("Store.Close released its retained path binding while a connection was checked out")
	}
	var singleton int
	if err := connection.QueryRowContext(context.Background(), `SELECT singleton FROM factory`).Scan(&singleton); err != nil || singleton != 1 {
		t.Fatalf("checked-out connection could not finish while Close retained authority: singleton=%d err=%v", singleton, err)
	}
	select {
	case err := <-closeResult:
		t.Fatalf("Store.Close returned before the checked-out connection was returned: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-closeResult; err != nil {
		t.Fatal(err)
	}
	if store.pathBinding != nil {
		t.Fatal("Store.Close retained its path binding after the checked-out connection returned")
	}
}

func TestStoreCloseUncertaintyRetainsExactAuthority(t *testing.T) {
	for _, failureKind := range []string{"reader", "writer"} {
		t.Run(failureKind, func(t *testing.T) {
			testStoreCloseUncertaintyRetainsExactAuthority(t, failureKind)
		})
	}
}

func testStoreCloseUncertaintyRetainsExactAuthority(t *testing.T, failureKind string) {
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
	defer func() {
		sqlitePoolCloseHook = nil
		_ = store.readers.Close()
		_ = store.writer.Close()
		if store.pathBinding != nil {
			_ = store.pathBinding.Close()
			store.pathBinding = nil
		}
	}()

	if err := os.Rename(home, home+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	replacements := map[string][]byte{
		"":     []byte("replacement-main-on-close"),
		"-wal": []byte("replacement-wal-on-close"),
		"-shm": bytes.Repeat([]byte{6}, walIndexRegionSize),
	}
	for suffix, contents := range replacements {
		if err := os.WriteFile(path+suffix, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	injected := errors.New("injected sqlite pool close BUSY")
	sqlitePoolCloseHook = func(kind string) error {
		if kind == failureKind {
			return injected
		}
		return nil
	}
	first := store.Close()
	if !errors.Is(first, injected) || !errors.Is(first, ErrCorruptState) {
		t.Fatalf("Store.Close uncertainty = %v", first)
	}
	if store.pathBinding == nil || store.pathBinding.authority == nil || store.pathBinding.main.file == nil {
		t.Fatal("Store.Close released retained authority after pool close uncertainty")
	}
	if second := store.Close(); second != first {
		t.Fatalf("repeated Store.Close = %v, want stable %v", second, first)
	}
	for suffix, want := range replacements {
		got, err := os.ReadFile(path + suffix)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("replacement %q changed after uncertain close: %x, %v", suffix, got, err)
		}
	}
}

func TestRejectedOperationalOpenReturnsRetainedStoreOnCloseUncertainty(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
	writeSidecar(t, path, image, 0o600)
	home, err := os.Open(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	database, err := os.Open(path)
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	rejected := errors.New("injected post-activation rejection")
	closeBusy := errors.New("injected rejection close BUSY")
	sqliteActivationHook = func(string) error { return rejected }
	sqlitePoolCloseHook = func(kind string) error {
		if kind == "reader" {
			return closeBusy
		}
		return nil
	}
	store, openErr := OpenOperational(context.Background(), path, home, database)
	defer func() {
		sqliteActivationHook = nil
		sqlitePoolCloseHook = nil
		if store != nil {
			_ = store.readers.Close()
			_ = store.writer.Close()
			if store.pathBinding != nil {
				_ = store.pathBinding.Close()
				store.pathBinding = nil
			}
		}
	}()
	if store == nil || !errors.Is(openErr, rejected) || !errors.Is(openErr, closeBusy) {
		t.Fatalf("rejected operational open = %v, %v; want retained uncertain Store", store, openErr)
	}
	if store.pathBinding == nil || store.pathBinding.authority == nil {
		t.Fatal("rejected operational open orphaned its retained authority")
	}
	if closeErr := store.Close(); !errors.Is(closeErr, closeBusy) {
		t.Fatalf("retained rejected Store.Close = %v", closeErr)
	}
}

func TestPartialReaderActivationRetainsWriterAndExactAuthorityOnCloseUncertainty(t *testing.T) {
	path, _ := walSnapshotFixture(t, "")
	home, database := openOperationalDescriptors(t, path)
	activationFailure := errors.New("injected reader activation failure")
	closeBusy := errors.New("injected partial writer close BUSY")
	var replacements map[string][]byte
	sqliteConnectHook = func(kind string, opened int) error {
		if kind == "reader verified" && opened == 1 {
			replacements = replaceSQLiteHomeForActivationTest(t, path, "partial-reader")
			return activationFailure
		}
		return nil
	}
	sqlitePoolCloseHook = func(kind string) error {
		if kind == "writer" {
			return closeBusy
		}
		return nil
	}
	var store *Store
	t.Cleanup(func() {
		sqliteConnectHook = nil
		sqlitePoolCloseHook = nil
		releaseRetainedStoreForTest(store)
	})

	store, err := OpenOperational(context.Background(), path, home, database)
	if store == nil || !errors.Is(err, activationFailure) || !errors.Is(err, closeBusy) || !errors.Is(err, ErrCorruptState) {
		t.Fatalf("partial reader activation = %v, %v; want retained uncertain Store", store, err)
	}
	if store.writer == nil || store.readers != nil || store.pathBinding == nil || store.pathBinding.main.file == nil {
		t.Fatal("partial reader activation discarded its writer or retained file authority")
	}
	assertRetainedDatabaseFilesLive(t, store.pathBinding)
	first := store.Close()
	if !errors.Is(first, closeBusy) {
		t.Fatalf("partial Store.Close = %v, want injected BUSY", first)
	}
	if second := store.Close(); second != first {
		t.Fatalf("repeated partial Store.Close = %v, want stable %v", second, first)
	}
	assertReplacementSQLiteHomeUnchanged(t, path, replacements)
}

func TestPhysicalPoolActivationFailureRetainsOwnerWhenPoolCloseIsUncertain(t *testing.T) {
	path, _ := walSnapshotFixture(t, "")
	home, database := openOperationalDescriptors(t, path)
	activationFailure := errors.New("injected physical writer activation failure")
	closeBusy := errors.New("injected physical writer pool close BUSY")
	var replacements map[string][]byte
	sqliteConnectHook = func(kind string, opened int) error {
		if kind == "writer" && opened == 1 {
			replacements = replaceSQLiteHomeForActivationTest(t, path, "physical-writer")
			return activationFailure
		}
		return nil
	}
	sqlitePoolCloseHook = func(kind string) error {
		if kind == "writer" {
			return closeBusy
		}
		return nil
	}
	var store *Store
	t.Cleanup(func() {
		sqliteConnectHook = nil
		sqlitePoolCloseHook = nil
		releaseRetainedStoreForTest(store)
	})

	store, err := OpenOperational(context.Background(), path, home, database)
	if store == nil || !errors.Is(err, activationFailure) || !errors.Is(err, closeBusy) || !errors.Is(err, ErrCorruptState) {
		t.Fatalf("physical writer activation = %v, %v; want retained uncertain Store", store, err)
	}
	if store.writer == nil || store.pathBinding == nil || store.pathBinding.authority == nil {
		t.Fatal("physical pool close uncertainty discarded its concrete pool/files owner")
	}
	assertRetainedDatabaseFilesLive(t, store.pathBinding)
	first := store.Close()
	if !errors.Is(first, closeBusy) {
		t.Fatalf("retained physical pool Close = %v, want injected BUSY", first)
	}
	if second := store.Close(); second != first {
		t.Fatalf("repeated physical pool Close = %v, want stable %v", second, first)
	}
	assertReplacementSQLiteHomeUnchanged(t, path, replacements)
}

func TestPostPoolRejectionRetainsFilesWhenPoolCloseIsUncertain(t *testing.T) {
	path, _ := walSnapshotFixture(t, "")
	home, database := openOperationalDescriptors(t, path)
	rejected := errors.New("injected post-pool rejection")
	closeBusy := errors.New("injected post-pool reader close BUSY")
	var replacements map[string][]byte
	sqlitePostPoolHook = func(point string) error {
		if point != "before sidecar refresh" {
			t.Fatalf("post-pool hook point = %q", point)
		}
		replacements = replaceSQLiteHomeForActivationTest(t, path, "post-pool")
		return rejected
	}
	sqlitePoolCloseHook = func(kind string) error {
		if kind == "reader" {
			return closeBusy
		}
		return nil
	}
	var store *Store
	t.Cleanup(func() {
		sqlitePostPoolHook = nil
		sqlitePoolCloseHook = nil
		releaseRetainedStoreForTest(store)
	})

	store, err := OpenOperational(context.Background(), path, home, database)
	if store == nil || !errors.Is(err, rejected) || !errors.Is(err, closeBusy) || !errors.Is(err, ErrCorruptState) {
		t.Fatalf("post-pool rejection = %v, %v; want retained uncertain Store", store, err)
	}
	if store.pathBinding == nil || store.pathBinding.main.file == nil || store.pathBinding.wal == nil || store.pathBinding.shm == nil {
		t.Fatal("post-pool rejection released the exact main/WAL/SHM authority")
	}
	assertRetainedDatabaseFilesLive(t, store.pathBinding)
	first := store.Close()
	if !errors.Is(first, closeBusy) {
		t.Fatalf("post-pool retained Store.Close = %v, want injected BUSY", first)
	}
	if second := store.Close(); second != first {
		t.Fatalf("repeated post-pool Store.Close = %v, want stable %v", second, first)
	}
	assertReplacementSQLiteHomeUnchanged(t, path, replacements)
}

func TestFiniteConnectorRetainsRawConnectionForgottenByDatabaseSQLClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "close-uncertain.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	base, err := (&sqliteDriver.SQLite{}).OpenConnector(configuredDataSource(path))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	driverConnection, ok := raw.(sqliteDriver.Conn)
	if !ok {
		_ = raw.Close()
		t.Fatalf("sqlite connection has type %T", raw)
	}
	closeBusy := errors.New("injected raw sqlite close BUSY")
	failing := &closeFailingSQLiteConnection{Conn: driverConnection, err: closeBusy}
	source := &singleSQLiteConnectionConnector{connection: failing}
	connector := &finiteConnector{
		Connector:     source,
		kind:          "raw retention test",
		remaining:     1,
		recheck:       func() error { return nil },
		activationCtx: context.Background(),
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = driverConnection.Close() })

	checkedOut, err := pool.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := checkedOut.Close(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); !errors.Is(err, closeBusy) {
		t.Fatalf("database/sql pool Close = %v, want raw close BUSY", err)
	}
	if failing.closeCalls != 1 {
		t.Fatalf("raw Close calls = %d, want one", failing.closeCalls)
	}
	if stats := pool.Stats(); stats.OpenConnections != 0 {
		t.Fatalf("database/sql retained %d open connections after failed raw Close", stats.OpenConnections)
	}
	if err := pool.Close(); err != nil {
		t.Fatalf("repeated database/sql pool Close = %v, want nil after it forgot the raw connection", err)
	}
	if failing.closeCalls != 1 {
		t.Fatalf("database/sql retried raw Close %d times, want one", failing.closeCalls)
	}
	if source.connects != 1 {
		t.Fatalf("physical Connect calls = %d, want one", source.connects)
	}
	connector.mu.Lock()
	retained := len(connector.connections) == 1 && connector.connections[0] == failing
	connector.mu.Unlock()
	if !retained {
		t.Fatalf("finite connector retained raw connections = %#v, want exact %p", connector.connections, failing)
	}
	if err := failing.Raw().Exec("SELECT 1"); err != nil {
		t.Fatalf("exact retained raw sqlite connection is not live after database/sql forgot it: %v", err)
	}
}

type closeFailingSQLiteConnection struct {
	sqliteDriver.Conn
	err        error
	closeCalls int
}

func (connection *closeFailingSQLiteConnection) Close() error {
	connection.closeCalls++
	return connection.err
}

type singleSQLiteConnectionConnector struct {
	connection sqldriver.Conn
	connects   int
}

func (connector *singleSQLiteConnectionConnector) Connect(context.Context) (sqldriver.Conn, error) {
	connector.connects++
	if connector.connects != 1 {
		return nil, errors.New("unexpected second physical sqlite connection")
	}
	return connector.connection, nil
}

func (*singleSQLiteConnectionConnector) Driver() sqldriver.Driver {
	return &sqliteDriver.SQLite{}
}

func openOperationalDescriptors(t *testing.T, path string) (*os.File, *os.File) {
	t.Helper()
	home, err := os.Open(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	database, err := os.Open(path)
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	return home, database
}

func replaceSQLiteHomeForActivationTest(t *testing.T, path, label string) map[string][]byte {
	t.Helper()
	home := filepath.Dir(path)
	if err := os.Rename(home, home+"."+label+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	replacements := map[string][]byte{
		"":     []byte("replacement-main-" + label),
		"-wal": []byte("replacement-wal-" + label),
		"-shm": bytes.Repeat([]byte{byte(len(label) + 1)}, walIndexRegionSize),
	}
	for suffix, contents := range replacements {
		if err := os.WriteFile(path+suffix, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return replacements
}

func assertReplacementSQLiteHomeUnchanged(t *testing.T, path string, replacements map[string][]byte) {
	t.Helper()
	if len(replacements) != 3 {
		t.Fatalf("replacement set has %d members, want main/WAL/SHM", len(replacements))
	}
	for suffix, want := range replacements {
		got, err := os.ReadFile(path + suffix)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("replacement %q changed during refused activation: %x, %v", suffix, got, err)
		}
	}
}

func assertRetainedDatabaseFilesLive(t *testing.T, files *databaseFiles) {
	t.Helper()
	if files == nil || files.authority == nil || files.directory == nil || files.main == nil || files.wal == nil || files.shm == nil {
		t.Fatal("retained database file set is incomplete")
	}
	for label, file := range map[string]*os.File{
		"directory": files.directory,
		"main":      files.main.file,
		"WAL":       files.wal.file,
		"SHM":       files.shm.file,
	} {
		if file == nil {
			t.Fatalf("retained %s descriptor is nil", label)
		}
		if _, err := file.Stat(); err != nil {
			t.Fatalf("retained %s descriptor is not live: %v", label, err)
		}
	}
	for index := range files.authority.components {
		if _, err := files.authority.components[index].file.Stat(); err != nil {
			t.Fatalf("retained path component %d is not live: %v", index, err)
		}
	}
}

func releaseRetainedStoreForTest(store *Store) {
	if store == nil {
		return
	}
	if store.readers != nil {
		_ = store.readers.Close()
	}
	if store.writer != nil {
		_ = store.writer.Close()
	}
	if store.pathBinding != nil {
		_ = store.pathBinding.Close()
		store.pathBinding = nil
	}
}
