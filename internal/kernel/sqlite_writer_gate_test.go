package kernel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
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
