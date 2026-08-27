package kernel

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/serdes"
	sqliteIO "github.com/ncruces/go-sqlite3/util/ioutil"
	"github.com/ncruces/go-sqlite3/vfs/readervfs"
	"golang.org/x/sys/unix"
)

const (
	busyMilliseconds              = 5000
	maxReaders                    = 4
	driverName                    = "sqlite3"
	maxFreshDatabaseImageSize     = 8 << 20
	maxImmutableDatabaseImageSize = 256 << 20
	maxSQLiteWALSize              = 272 << 20
	maxSQLiteSHMSize              = 4 << 20
	walHeaderSize                 = 32
	walFormatVersion              = 3007000
	walIndexRegionSize            = 32768
)

var immutableReaderSequence atomic.Uint64

type Store struct {
	writer     *sql.DB
	readers    *sql.DB
	writerGate chan struct{}
	closed     atomic.Bool
	close      sync.Once
	closeErr   error
}

func Create(ctx context.Context, absolutePath string, config FactoryConfig, at UnixMillis) (*Store, error) {
	if err := validateDatabasePath(absolutePath); err != nil {
		return nil, err
	}
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(absolutePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("reserve fresh sqlite database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close fresh sqlite reservation: %w", err)
	}
	reservation, err := openDatabaseParent(absolutePath)
	if err != nil {
		return nil, err
	}
	if err := reservation.reservePrivateSHM(); err != nil {
		return nil, errors.Join(err, reservation.Close())
	}
	store, err := openPools(absolutePath)
	if err != nil {
		return nil, errors.Join(err, reservation.removeEmptyCreatedSidecars(), reservation.Close())
	}
	if err := reservation.Close(); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.initialize(ctx, config, at); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.validateOpen(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// NewDatabaseImage creates one complete fresh database without touching the
// filesystem. The returned main-database image uses SQLite's rollback header;
// Open validates the complete image before promoting it to persistent WAL.
func NewDatabaseImage(ctx context.Context, config FactoryConfig, at UnixMillis) (image []byte, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	pool, err := sql.Open(driverName, ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open in-memory sqlite database: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	defer func() {
		if err := pool.Close(); err != nil {
			image = nil
			resultErr = errors.Join(resultErr, fmt.Errorf("close in-memory sqlite database: %w", err))
		}
	}()

	connection, err := pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkout in-memory sqlite connection: %w", err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			image = nil
			resultErr = errors.Join(resultErr, fmt.Errorf("return in-memory sqlite connection: %w", err))
		}
	}()
	if _, err := connection.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("configure in-memory sqlite database: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "PRAGMA temp_store = MEMORY"); err != nil {
		return nil, fmt.Errorf("configure in-memory sqlite temporary storage: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("begin in-memory sqlite initialization: %w", err)
	}
	if err := initializeFresh(ctx, connection, config, at); err != nil {
		_, rollbackErr := connection.ExecContext(context.Background(), "ROLLBACK")
		return nil, errors.Join(err, rollbackErr)
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("commit in-memory sqlite initialization: %w", err)
	}
	if err := validateDatabaseSnapshot(ctx, connection); err != nil {
		return nil, err
	}
	if err := connection.Raw(func(raw any) error {
		driverConnection, ok := raw.(sqliteDriver.Conn)
		if !ok {
			return errors.New("sqlite driver does not expose its raw connection")
		}
		var serializeErr error
		image, serializeErr = serdes.Serialize(driverConnection.Raw(), "main")
		return serializeErr
	}); err != nil {
		return nil, fmt.Errorf("serialize fresh sqlite database: %w", err)
	}
	if len(image) == 0 || len(image) > maxFreshDatabaseImageSize {
		return nil, fmt.Errorf("%w: fresh sqlite image size %d outside 1..%d", ErrCorruptState, len(image), maxFreshDatabaseImageSize)
	}
	if err := InspectImmutable(ctx, bytes.NewReader(image), int64(len(image))); err != nil {
		return nil, fmt.Errorf("validate serialized sqlite database: %w", err)
	}
	return image, nil
}

// InspectImmutable validates an already-open, sidecar-free SQLite main
// database without taking ownership of reader and without any write-capable
// Store or filesystem path. The caller owns lifetime locking and sidecar
// policy and must keep reader's bytes stable until this call returns.
func InspectImmutable(ctx context.Context, reader io.ReaderAt, size int64) error {
	return inspectImmutable(ctx, reader, size, false)
}

func inspectImmutable(ctx context.Context, reader io.ReaderAt, size int64, requirePristine bool) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reader == nil || size <= 0 {
		return fmt.Errorf("%w: immutable sqlite reader and positive size are required", ErrInvalidValue)
	}
	if size > maxImmutableDatabaseImageSize {
		return fmt.Errorf("%w: immutable sqlite image size %d exceeds %d", ErrInvalidValue, size, maxImmutableDatabaseImageSize)
	}
	exactReader := &immutableReaderAt{reader: reader, size: size}
	if err := validateDatabaseHeader(exactReader, size, false); err != nil {
		return err
	}
	name, err := reserveImmutableReaderName()
	if err != nil {
		return err
	}
	readervfs.Create(name, sqliteIO.NewSizeReaderAt(exactReader))

	query := url.Values{
		"_pragma": {"query_only(ON)", "temp_store(MEMORY)"},
		"vfs":     {"reader"},
	}
	pool, err := sql.Open(driverName, "file:"+name+"?"+query.Encode())
	if err != nil {
		readervfs.Delete(name)
		return fmt.Errorf("open immutable sqlite reader: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	defer func() {
		closeErr := pool.Close()
		readerErr := exactReader.firstError()
		readervfs.Delete(name)
		if closeErr != nil {
			closeErr = fmt.Errorf("close immutable sqlite reader: %w", closeErr)
		}
		resultErr = errors.Join(resultErr, closeErr, readerErr)
	}()
	connection, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("checkout immutable sqlite reader: %w", err)
	}
	tx, err := beginPinnedRead(ctx, connection)
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("begin immutable sqlite inspection: %w", err)
	}
	err = validateDatabaseSnapshot(ctx, tx.connection)
	if err == nil && requirePristine {
		err = validatePristineBootstrap(ctx, tx.connection)
	}
	return errors.Join(err, tx.Close())
}

func reserveImmutableReaderName() (string, error) {
	for {
		current := immutableReaderSequence.Load()
		if current == math.MaxUint64 {
			return "", fmt.Errorf("%w: immutable sqlite reader names exhausted", ErrCorruptState)
		}
		next := current + 1
		if immutableReaderSequence.CompareAndSwap(current, next) {
			return fmt.Sprintf("dark-factory-kernel-%d", next), nil
		}
	}
}

// immutableReaderAt is the one boundary between caller-owned bytes and
// SQLite. It enforces the declared extent, turns illegal short reads into a
// real error, and retains errors that SQLite's VFS error mapping cannot carry
// back through database/sql.
type immutableReaderAt struct {
	reader io.ReaderAt
	size   int64

	mu  sync.Mutex
	err error
}

func (reader *immutableReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if len(buffer) == 0 {
		if offset < 0 || offset > reader.size {
			return 0, io.EOF
		}
		return 0, nil
	}
	if offset < 0 || offset >= reader.size {
		return 0, io.EOF
	}
	want := len(buffer)
	available := reader.size - offset
	if int64(want) > available {
		want = int(available)
	}
	read, err := reader.reader.ReadAt(buffer[:want], offset)
	if read < 0 || read > want {
		read = 0
		err = io.ErrUnexpectedEOF
	} else if read != want && err == nil {
		err = io.ErrUnexpectedEOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		reader.recordError(err)
	}
	if err == nil && want != len(buffer) {
		err = io.EOF
	}
	return read, err
}

func (reader *immutableReaderAt) Size() (int64, error) {
	return reader.size, nil
}

func (reader *immutableReaderAt) recordError(err error) {
	reader.mu.Lock()
	if reader.err == nil {
		reader.err = err
	}
	reader.mu.Unlock()
}

func (reader *immutableReaderAt) firstError() error {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.err == nil {
		return nil
	}
	return fmt.Errorf("read immutable sqlite image: %w", reader.err)
}

func Open(ctx context.Context, absolutePath string) (*Store, error) {
	if err := validateDatabasePath(absolutePath); err != nil {
		return nil, err
	}
	files, err := openDatabaseFiles(absolutePath)
	if err != nil {
		return nil, err
	}
	if err := preflightExisting(ctx, files); err != nil {
		return nil, errors.Join(err, files.Close())
	}
	if err := files.recheckPaths(); err != nil {
		return nil, errors.Join(err, files.Close())
	}
	if files.wal == nil {
		if err := files.reservePrivateSHM(); err != nil {
			return nil, errors.Join(err, files.Close())
		}
	}
	files.realOpenStarted = true
	store, err := openPools(absolutePath)
	if err != nil {
		return nil, errors.Join(err, files.removeEmptyCreatedSidecars(), files.Close())
	}
	if files.wal == nil {
		files.wal, err = files.openDatabaseFile(files.main.name+"-wal", "WAL", 0, maxSQLiteWALSize)
		if err != nil {
			_ = store.Close()
			return nil, errors.Join(err, files.Close())
		}
	}
	if err := store.validateOpen(ctx); err != nil {
		_ = store.Close()
		return nil, errors.Join(err, files.Close())
	}
	if err := files.recheckPaths(); err != nil {
		_ = store.Close()
		return nil, errors.Join(err, files.Close())
	}
	if err := files.Close(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func validateDatabasePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: sqlite path must be absolute: %q", ErrInvalidValue, path)
	}
	return nil
}

func preflightExisting(ctx context.Context, files *databaseFiles) error {
	if files.wal == nil {
		header := make([]byte, 20)
		if _, err := files.main.file.ReadAt(header, 0); err != nil {
			return fmt.Errorf("%w: read sqlite header: %w", ErrForeignDatabase, err)
		}
		if err := validateJournalHeaderBytes(header, false); err != nil {
			return err
		}
		return inspectImmutable(ctx, files.main.file, files.main.info.Size(), header[18] == 1)
	}
	return validateWALSnapshotCopy(ctx, files)
}

type databaseFile struct {
	path    string
	name    string
	file    *os.File
	info    os.FileInfo
	stat    unix.Stat_t
	minimum int64
	maximum int64
}

type databaseFiles struct {
	directoryPath   string
	directory       *os.File
	directoryStat   unix.Stat_t
	main            *databaseFile
	wal             *databaseFile
	shm             *databaseFile
	reservedSHM     *databaseFile
	preexistingWAL  bool
	realOpenStarted bool
}

func openDatabaseFiles(path string) (_ *databaseFiles, resultErr error) {
	directoryPath := filepath.Dir(path)
	base := filepath.Base(path)
	fd, err := unix.Open(directoryPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open sqlite parent directory: %w", err)
	}
	directory := os.NewFile(uintptr(fd), directoryPath)
	if directory == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open sqlite parent directory: invalid file descriptor")
	}
	files := &databaseFiles{directoryPath: directoryPath, directory: directory}
	if err := unix.Fstat(fd, &files.directoryStat); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect sqlite parent directory: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, files.Close())
		}
	}()

	journalPresent, err := files.pathPresent(base + "-journal")
	if err != nil {
		return nil, err
	}
	if journalPresent {
		return nil, fmt.Errorf("%w: rollback journal requires recovery", ErrCorruptState)
	}
	walPresent, err := files.pathPresent(base + "-wal")
	if err != nil {
		return nil, err
	}
	shmPresent, err := files.pathPresent(base + "-shm")
	if err != nil {
		return nil, err
	}
	if walPresent != shmPresent {
		return nil, fmt.Errorf("%w: incomplete WAL sidecar pair", ErrCorruptState)
	}
	files.preexistingWAL = walPresent

	files.main, err = files.openDatabaseFile(base, "main database", 100, maxImmutableDatabaseImageSize)
	if err != nil {
		return nil, err
	}
	if err := validateMainFile(files.main, walPresent); err != nil {
		return nil, err
	}
	if !walPresent {
		return files, nil
	}
	files.wal, err = files.openDatabaseFile(base+"-wal", "WAL", 0, maxSQLiteWALSize)
	if err != nil {
		return nil, err
	}
	files.shm, err = files.openDatabaseFile(base+"-shm", "SHM", walIndexRegionSize, maxSQLiteSHMSize)
	if err != nil {
		return nil, err
	}
	if files.shm.info.Size()%walIndexRegionSize != 0 {
		return nil, fmt.Errorf("%w: SHM size %d is not a positive multiple of %d", ErrCorruptState, files.shm.info.Size(), walIndexRegionSize)
	}
	pageSize, err := databasePageSize(files.main.file)
	if err != nil {
		return nil, err
	}
	if err := validateWAL(files.wal.file, files.wal.info.Size(), pageSize); err != nil {
		return nil, err
	}
	return files, nil
}

func (files *databaseFiles) openDatabaseFile(name, kind string, minimum, maximum int64) (*databaseFile, error) {
	fd, err := unix.Openat(int(files.directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: open sqlite %s: %v", ErrForeignDatabase, kind, err)
	}
	path := filepath.Join(files.directoryPath, name)
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open sqlite %s: invalid file descriptor", kind)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect sqlite %s: %w", kind, err), file.Close())
	}
	if err := validateDatabaseFileInfo(info, kind, minimum, maximum); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, errors.Join(fmt.Errorf("inspect sqlite %s identity: %w", kind, err), file.Close())
	}
	return &databaseFile{path: path, name: name, file: file, info: info, stat: stat, minimum: minimum, maximum: maximum}, nil
}

func validateDatabaseFileInfo(info os.FileInfo, kind string, minimum, maximum int64) error {
	if info.Mode() != 0o600 {
		return fmt.Errorf("%w: sqlite %s mode is %v, want exact regular 0600", ErrForeignDatabase, kind, info.Mode())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return fmt.Errorf("%w: sqlite %s owner or link identity is unsafe", ErrForeignDatabase, kind)
	}
	if info.Size() < minimum || info.Size() > maximum {
		return fmt.Errorf("%w: sqlite %s size %d outside %d..%d", ErrForeignDatabase, kind, info.Size(), minimum, maximum)
	}
	return nil
}

func validateMainFile(main *databaseFile, walRequired bool) error {
	if err := validateJournalHeader(main.file, walRequired); err != nil {
		return err
	}
	pageSize, err := databasePageSize(main.file)
	if err != nil {
		return err
	}
	if main.info.Size()%int64(pageSize) != 0 {
		return fmt.Errorf("%w: main database size is not page aligned", ErrCorruptState)
	}
	return nil
}

func databasePageSize(reader io.ReaderAt) (uint32, error) {
	header := make([]byte, 18)
	if _, err := reader.ReadAt(header, 0); err != nil {
		return 0, fmt.Errorf("%w: read sqlite page size: %w", ErrForeignDatabase, err)
	}
	pageSize := uint32(binary.BigEndian.Uint16(header[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return 0, fmt.Errorf("%w: invalid sqlite page size", ErrCorruptState)
	}
	return pageSize, nil
}

func (files *databaseFiles) pathPresent(name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(files.directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	return false, fmt.Errorf("inspect sqlite path: %w", err)
}

func (files *databaseFiles) recheckPaths() error {
	if err := files.recheckDirectory(); err != nil {
		return err
	}
	if present, err := files.pathPresent(files.main.name + "-journal"); err != nil {
		return err
	} else if present {
		return fmt.Errorf("%w: rollback journal appeared during sqlite preflight", ErrCorruptState)
	}
	for _, source := range []*databaseFile{files.main, files.wal, files.shm, files.reservedSHM} {
		if source == nil {
			continue
		}
		current, err := source.file.Stat()
		if err != nil {
			return fmt.Errorf("recheck pinned sqlite file: %w", err)
		}
		if !os.SameFile(source.info, current) {
			return fmt.Errorf("%w: pinned sqlite file identity changed during preflight", ErrCorruptState)
		}
		if !files.preexistingWAL && !files.realOpenStarted && source == files.main && (current.Size() != source.info.Size() || !current.ModTime().Equal(source.info.ModTime())) {
			return fmt.Errorf("%w: standalone sqlite main file changed during preflight", ErrCorruptState)
		}
		if err := validateDatabaseFileInfo(current, filepath.Base(source.path), source.minimum, source.maximum); err != nil {
			return err
		}
		var binding unix.Stat_t
		if err := unix.Fstatat(int(files.directory.Fd()), source.name, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return fmt.Errorf("recheck sqlite path binding: %w", err)
		}
		if binding.Dev != source.stat.Dev || binding.Ino != source.stat.Ino {
			return fmt.Errorf("%w: sqlite path binding changed during preflight", ErrCorruptState)
		}
	}
	for name, expected := range map[string]bool{files.main.name + "-wal": files.wal != nil, files.main.name + "-shm": files.shm != nil || files.reservedSHM != nil} {
		present, err := files.pathPresent(name)
		if err != nil {
			return err
		}
		if present != expected {
			return fmt.Errorf("%w: sqlite sidecar set changed during preflight", ErrCorruptState)
		}
	}
	return nil
}

func (files *databaseFiles) recheckDirectory() error {
	fd, err := unix.Open(files.directoryPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("reopen sqlite parent directory: %w", err)
	}
	defer unix.Close(fd)
	var current unix.Stat_t
	if err := unix.Fstat(fd, &current); err != nil {
		return fmt.Errorf("recheck sqlite parent directory: %w", err)
	}
	if current.Dev != files.directoryStat.Dev || current.Ino != files.directoryStat.Ino {
		return fmt.Errorf("%w: sqlite parent directory identity changed during preflight", ErrCorruptState)
	}
	return nil
}

func (files *databaseFiles) Close() error {
	if files == nil {
		return nil
	}
	var result error
	for _, source := range []*databaseFile{files.reservedSHM, files.shm, files.wal, files.main} {
		if source != nil && source.file != nil {
			result = errors.Join(result, source.file.Close())
			source.file = nil
		}
	}
	if files.directory != nil {
		result = errors.Join(result, files.directory.Close())
		files.directory = nil
	}
	return result
}

func openDatabaseParent(path string) (*databaseFiles, error) {
	directoryPath := filepath.Dir(path)
	fd, err := unix.Open(directoryPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, fmt.Errorf("open sqlite parent directory: %w", err)
	}
	directory := os.NewFile(uintptr(fd), directoryPath)
	if directory == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open sqlite parent directory: invalid file descriptor")
	}
	files := &databaseFiles{directoryPath: directoryPath, directory: directory, main: &databaseFile{name: filepath.Base(path), path: path}}
	if err := unix.Fstat(fd, &files.directoryStat); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect sqlite parent directory: %w", err)
	}
	return files, nil
}

func (files *databaseFiles) removeEmptyCreatedSidecars() error {
	if files == nil || files.preexistingWAL {
		return nil
	}
	var result error
	for _, name := range []string{files.main.name + "-wal", files.main.name + "-shm"} {
		fd, err := unix.Openat(int(files.directory.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("open failed sqlite sidecar cleanup target: %w", err))
			continue
		}
		var pinned, binding unix.Stat_t
		statErr := unix.Fstat(fd, &pinned)
		bindErr := unix.Fstatat(int(files.directory.Fd()), name, &binding, unix.AT_SYMLINK_NOFOLLOW)
		if statErr == nil && bindErr == nil && pinned.Dev == binding.Dev && pinned.Ino == binding.Ino && pinned.Size == 0 {
			bindErr = unix.Unlinkat(int(files.directory.Fd()), name, 0)
		}
		result = errors.Join(result, statErr, bindErr, unix.Close(fd))
	}
	return result
}

func (files *databaseFiles) reservePrivateSHM() error {
	name := files.main.name + "-shm"
	fd, err := unix.Openat(int(files.directory.Fd()), name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("reserve private sqlite SHM: %w", err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(files.directoryPath, name))
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("reserve private sqlite SHM: invalid file descriptor")
	}
	info, err := file.Stat()
	if err != nil {
		return errors.Join(fmt.Errorf("inspect private sqlite SHM reservation: %w", err), file.Close())
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return errors.Join(fmt.Errorf("inspect private sqlite SHM identity: %w", err), file.Close())
	}
	files.reservedSHM = &databaseFile{name: name, path: filepath.Join(files.directoryPath, name), file: file, info: info, stat: stat, minimum: 0, maximum: maxSQLiteSHMSize}
	return nil
}

func validateWAL(reader io.ReaderAt, size int64, mainPageSize uint32) error {
	if size == 0 {
		return nil
	}
	if size < walHeaderSize || size > maxSQLiteWALSize {
		return fmt.Errorf("%w: WAL size %d outside 0 or %d..%d", ErrCorruptState, size, walHeaderSize, maxSQLiteWALSize)
	}
	header := make([]byte, walHeaderSize)
	if _, err := reader.ReadAt(header, 0); err != nil {
		return fmt.Errorf("%w: read WAL header: %w", ErrCorruptState, err)
	}
	magic := binary.BigEndian.Uint32(header[0:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return fmt.Errorf("%w: invalid WAL magic", ErrCorruptState)
	}
	if binary.BigEndian.Uint32(header[4:8]) != walFormatVersion {
		return fmt.Errorf("%w: unsupported WAL format", ErrCorruptState)
	}
	pageSize := binary.BigEndian.Uint32(header[8:12])
	if pageSize != mainPageSize {
		return fmt.Errorf("%w: WAL page size does not match main database", ErrCorruptState)
	}
	checksum := walChecksum(magic == 0x377f0683, header[:24], 0, 0)
	if checksum[0] != binary.BigEndian.Uint32(header[24:28]) || checksum[1] != binary.BigEndian.Uint32(header[28:32]) {
		return fmt.Errorf("%w: invalid WAL header checksum", ErrCorruptState)
	}
	// Frame validity is intentionally left to the pinned SQLite build against
	// the disposable copy. SQLite recovery ignores incomplete, checksum-invalid,
	// salt-mismatched, and reused physical tails after the last valid commit;
	// duplicating that recovery algorithm here would reject legitimate crashes.
	return nil
}

func walChecksum(bigEndian bool, data []byte, first, second uint32) [2]uint32 {
	var order binary.ByteOrder = binary.LittleEndian
	if bigEndian {
		order = binary.BigEndian
	}
	for offset := 0; offset+8 <= len(data); offset += 8 {
		first += order.Uint32(data[offset:offset+4]) + second
		second += order.Uint32(data[offset+4:offset+8]) + first
	}
	return [2]uint32{first, second}
}

func validateWALSnapshotCopy(ctx context.Context, sources *databaseFiles) (resultErr error) {
	directory, err := os.MkdirTemp("", "dark-factory-wal-preflight-")
	if err != nil {
		return fmt.Errorf("create private WAL preflight directory: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, os.RemoveAll(directory))
	}()
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure WAL preflight directory: %w", err)
	}
	if err := validatePrivateDirectory(directory); err != nil {
		return err
	}
	temporaryMain := filepath.Join(directory, "factory.sqlite3")
	if err := copyDatabaseFile(ctx, sources.main, temporaryMain); err != nil {
		return err
	}
	if err := copyDatabaseFile(ctx, sources.wal, temporaryMain+"-wal"); err != nil {
		return err
	}

	pool, err := sql.Open(driverName, walPreflightDataSource(temporaryMain))
	if err != nil {
		return fmt.Errorf("open isolated WAL snapshot: %w", err)
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	defer func() {
		if err := pool.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close isolated WAL snapshot: %w", err))
		}
	}()
	connection, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("checkout isolated WAL snapshot: %w", err)
	}
	tx, err := beginPinnedRead(ctx, connection)
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("begin isolated WAL snapshot: %w", err)
	}
	var journalMode string
	err = tx.connection.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode)
	if err == nil && strings.ToLower(journalMode) != "wal" {
		err = fmt.Errorf("%w: isolated database did not recover in WAL mode", ErrCorruptState)
	}
	if err == nil {
		err = validateDatabaseSnapshot(ctx, tx.connection)
	}
	return errors.Join(err, tx.Close())
}

func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private WAL preflight directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode() != os.ModeDir|0o700 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%w: WAL preflight directory is not exact owner-only 0700", ErrForeignDatabase)
	}
	return nil
}

func walPreflightDataSource(path string) string {
	query := url.Values{"mode": {"rw"}}
	query["_pragma"] = []string{
		fmt.Sprintf("busy_timeout(%d)", busyMilliseconds),
		"foreign_keys(ON)",
		"query_only(ON)",
		"temp_store(MEMORY)",
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func copyDatabaseFile(ctx context.Context, source *databaseFile, targetPath string) (resultErr error) {
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create isolated sqlite copy: %w", err)
	}
	defer func() {
		if err := target.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close isolated sqlite copy: %w", err))
		}
	}()
	const bufferSize = 128 << 10
	buffer := make([]byte, bufferSize)
	section := io.NewSectionReader(source.file, 0, source.info.Size())
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := section.Read(buffer)
		if read > 0 {
			written, writeErr := target.Write(buffer[:read])
			if writeErr != nil {
				return fmt.Errorf("write isolated sqlite copy: %w", writeErr)
			}
			if written != read {
				return fmt.Errorf("write isolated sqlite copy: %w", io.ErrShortWrite)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read sqlite source for isolated copy: %w", readErr)
		}
	}
	return nil
}

func validatePristineBootstrap(ctx context.Context, connection *sql.Conn) error {
	var rows int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM factory WHERE singleton = 1 AND revision = 1 AND next_invalidation_sequence = 1 AND invalidation_floor = 1`).Scan(&rows); err != nil {
		return fmt.Errorf("inspect bootstrap factory state: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: rollback database is not an exact fresh bootstrap", ErrForeignDatabase)
	}
	for name, object := range expectedSchema() {
		if object.kind != "table" || name == "factory" {
			continue
		}
		if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM "`+name+`" LIMIT 1)`).Scan(&rows); err != nil {
			return fmt.Errorf("inspect bootstrap table %s: %w", name, err)
		}
		if rows != 0 {
			return fmt.Errorf("%w: rollback database contains retained %s state", ErrForeignDatabase, name)
		}
	}
	if err := connection.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_sequence LIMIT 1)`).Scan(&rows); err != nil {
		return fmt.Errorf("inspect bootstrap sqlite sequence: %w", err)
	}
	if rows != 0 {
		return fmt.Errorf("%w: rollback database contains retained sequence state", ErrForeignDatabase)
	}
	return nil
}

func validateDatabaseHeader(reader io.ReaderAt, size int64, walRequired bool) error {
	header := make([]byte, 100)
	if _, err := reader.ReadAt(header, 0); err != nil {
		return fmt.Errorf("%w: read sqlite header: %w", ErrForeignDatabase, err)
	}
	if err := validateJournalHeaderBytes(header, walRequired); err != nil {
		return err
	}
	pageSize := int64(binary.BigEndian.Uint16(header[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	pageCount := int64(binary.BigEndian.Uint32(header[28:32]))
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 || pageCount < 1 || pageCount > (1<<63-1)/pageSize || pageCount*pageSize != size {
		return fmt.Errorf("%w: invalid sqlite page size or database length", ErrCorruptState)
	}
	return nil
}

func validateJournalHeader(reader io.ReaderAt, walRequired bool) error {
	header := make([]byte, 20)
	if _, err := reader.ReadAt(header, 0); err != nil {
		return fmt.Errorf("%w: read sqlite header: %w", ErrForeignDatabase, err)
	}
	return validateJournalHeaderBytes(header, walRequired)
}

func validateJournalHeaderBytes(header []byte, walRequired bool) error {
	if string(header[:16]) != "SQLite format 3\x00" {
		return fmt.Errorf("%w: invalid sqlite header", ErrForeignDatabase)
	}
	if header[18] != header[19] || header[18] != 1 && header[18] != 2 {
		return fmt.Errorf("%w: invalid database journal header", ErrCorruptState)
	}
	if walRequired && header[18] != 2 {
		return fmt.Errorf("%w: database with WAL sidecars does not have a WAL header", ErrCorruptState)
	}
	return nil
}

func openPools(path string) (*Store, error) {
	writer, err := openPool(path, 1)
	if err != nil {
		return nil, err
	}
	readers, err := openPool(path, maxReaders)
	if err != nil {
		_ = writer.Close()
		return nil, err
	}
	writerGate := make(chan struct{}, 1)
	writerGate <- struct{}{}
	store := &Store{writer: writer, readers: readers, writerGate: writerGate}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	writerConnection, err := store.writerConnection(ctx)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := writerConnection.Close(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("return initial writer connection: %w", err)
	}
	readerConnection, err := store.readerConnection(ctx)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := readerConnection.Close(); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("return initial reader connection: %w", err)
	}
	return store, nil
}

func openPool(path string, limit int) (*sql.DB, error) {
	pool, err := sql.Open(driverName, configuredDataSource(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite pool: %w", err)
	}
	pool.SetMaxOpenConns(limit)
	pool.SetMaxIdleConns(limit)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		_ = pool.Close()
		return nil, fmt.Errorf("initialize sqlite pool: %w", err)
	}
	return pool, nil
}

func configuredDataSource(path string) string {
	query := url.Values{}
	query.Set("modeof", path)
	query["_pragma"] = []string{
		fmt.Sprintf("busy_timeout(%d)", busyMilliseconds),
		"foreign_keys(ON)",
		"journal_mode(WAL)",
		"synchronous(FULL)",
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func (store *Store) writerConnection(ctx context.Context) (*sql.Conn, error) {
	return store.verifiedConnection(ctx, store.writer, "writer")
}

func (store *Store) readerConnection(ctx context.Context) (*sql.Conn, error) {
	return store.verifiedConnection(ctx, store.readers, "reader")
}

func (store *Store) verifiedConnection(ctx context.Context, pool *sql.DB, kind string) (*sql.Conn, error) {
	if store.closed.Load() {
		return nil, ErrStoreClosed
	}
	connection, err := pool.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("checkout %s connection: %w", kind, err)
	}
	if err := verifyConnection(ctx, connection); err != nil {
		discardConnection(connection)
		return nil, fmt.Errorf("verify %s connection: %w", kind, err)
	}
	return connection, nil
}

func verifyConnection(ctx context.Context, connection *sql.Conn) error {
	var foreignKeys, synchronous, busy int
	var journalMode string
	if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return err
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if err := connection.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		return err
	}
	if foreignKeys != 1 || strings.ToLower(journalMode) != "wal" || synchronous != 2 || busy != busyMilliseconds {
		return fmt.Errorf(
			"unsafe sqlite connection configuration: foreign_keys=%d journal_mode=%q synchronous=%d busy_timeout=%d",
			foreignKeys,
			journalMode,
			synchronous,
			busy,
		)
	}
	return nil
}

func (store *Store) initialize(ctx context.Context, config FactoryConfig, at UnixMillis) error {
	for attempt := 0; attempt < 2; attempt++ {
		tx, err := store.beginUncheckedWrite(ctx)
		if err != nil {
			return err
		}
		err = initializeFresh(ctx, tx.connection, config, at)
		if err != nil {
			result := tx.Rollback(err)
			tx.Close()
			return result
		}
		err = tx.Commit(ctx)
		tx.Close()
		if err == nil {
			return nil
		}
		var unknown *OutcomeUnknownError
		if !errors.As(err, &unknown) {
			return err
		}
		connection, checkoutErr := store.readerConnection(ctx)
		if checkoutErr != nil {
			return errors.Join(err, checkoutErr)
		}
		exactErr := validateExactSchema(ctx, connection)
		if exactErr == nil {
			_ = connection.Close()
			return nil
		}
		empty, emptyErr := schemaIsEmpty(ctx, connection)
		_ = connection.Close()
		if emptyErr != nil || !empty || attempt == 1 {
			return errors.Join(err, exactErr, emptyErr)
		}
	}
	return errors.New("unreachable sqlite initialization state")
}

func initializeFresh(ctx context.Context, connection *sql.Conn, config FactoryConfig, at UnixMillis) error {
	empty, err := schemaIsEmpty(ctx, connection)
	if err == nil && !empty {
		err = fmt.Errorf("%w: fresh database is not empty", ErrForeignDatabase)
	}
	if err == nil {
		_, err = connection.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID))
	}
	if err == nil {
		_, err = connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", userVersion))
	}
	for _, statement := range schemaStatements {
		if err != nil {
			break
		}
		_, err = connection.ExecContext(ctx, statement)
	}
	if err == nil {
		var rawDaemonID [IDBytes]byte
		if _, err = rand.Read(rawDaemonID[:]); err == nil && rawDaemonID == [IDBytes]byte{} {
			err = fmt.Errorf("%w: generated zero daemon identifier", ErrCorruptState)
		}
		if err == nil {
			inserted, insertErr := connection.ExecContext(ctx,
				`INSERT INTO factory(singleton, daemon_id, dispatch_enabled, capacity, revision, next_invalidation_sequence, invalidation_floor, updated_at_ms) VALUES(1, ?, ?, ?, 1, 1, 1, ?)`,
				rawDaemonID[:], boolInt(config.DispatchEnabled), int64(config.Capacity), at.Int64())
			err = requireOneRow(inserted, insertErr)
		}
	}
	return err
}

func (store *Store) validateOpen(ctx context.Context) error {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return err
	}
	err = validateDatabaseSnapshot(ctx, tx.connection)
	return errors.Join(err, tx.Close())
}

func validateDatabaseSnapshot(ctx context.Context, connection *sql.Conn) error {
	if err := validateExactSchema(ctx, connection); err != nil {
		return err
	}
	if err := validateDurableControls(ctx, connection); err != nil {
		return err
	}
	return validateIntegrity(ctx, connection)
}

func (store *Store) Close() error {
	store.close.Do(func() {
		store.closed.Store(true)
		// Wait for the one already-admitted writer, if any, before closing
		// either pool. New writers observe closed after acquiring the gate and
		// leave without touching SQLite.
		if err := store.acquireWriter(context.Background()); err != nil {
			store.closeErr = err
			return
		}
		defer store.releaseWriter()
		store.closeErr = errors.Join(store.readers.Close(), store.writer.Close())
	})
	return store.closeErr
}

type writeTx struct {
	store          *Store
	connection     *sql.Conn
	active         bool
	discarded      bool
	closed         bool
	writerAdmitted bool
}

func (store *Store) beginValidatedWrite(ctx context.Context) (*writeTx, error) {
	tx, err := store.beginUncheckedWrite(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		err = tx.Rollback(err)
		tx.Close()
		return nil, err
	}
	return tx, nil
}

// beginUncheckedWrite exists only because a fresh database has no durable
// graph to validate until initialize creates it. Public mutations must enter
// through beginValidatedWrite.
func (store *Store) beginUncheckedWrite(ctx context.Context) (*writeTx, error) {
	if err := store.acquireWriter(ctx); err != nil {
		return nil, err
	}
	if store.closed.Load() {
		store.releaseWriter()
		return nil, ErrStoreClosed
	}
	connection, err := store.writerConnection(ctx)
	if err != nil {
		store.releaseWriter()
		return nil, err
	}
	tx := &writeTx{store: store, connection: connection, writerAdmitted: true}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		if cancellation := ctx.Err(); cancellation != nil {
			tx.discard()
			tx.Close()
			return nil, &OutcomeUnknownError{cause: errors.Join(cancellation, fmt.Errorf("begin immediate: %w", err))}
		}
		if errors.Is(err, sqlite3.BUSY) || errors.Is(err, sqlite3.LOCKED) {
			tx.Close()
			return nil, fmt.Errorf("%w: %v", ErrBusy, err)
		}
		tx.discard()
		tx.Close()
		return nil, &OutcomeUnknownError{cause: fmt.Errorf("begin immediate: %w", err)}
	}
	tx.active = true
	return tx, nil
}

func (tx *writeTx) Commit(ctx context.Context) error {
	if !tx.active {
		return errors.New("commit inactive sqlite write")
	}
	if err := ctx.Err(); err != nil {
		return tx.Rollback(err)
	}
	if _, err := tx.connection.ExecContext(ctx, "COMMIT"); err != nil {
		tx.active = false
		tx.discard()
		return &OutcomeUnknownError{cause: fmt.Errorf("commit: %w", err)}
	}
	tx.active = false
	return nil
}

func (tx *writeTx) Rollback(cause error) error {
	if !tx.active {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Duration(busyMilliseconds)*time.Millisecond)
	defer cancel()
	if _, err := tx.connection.ExecContext(ctx, "ROLLBACK"); err != nil {
		tx.active = false
		tx.discard()
		return &OutcomeUnknownError{cause: errors.Join(cause, fmt.Errorf("rollback: %w", err))}
	}
	tx.active = false
	return cause
}

func (tx *writeTx) discard() {
	if tx.discarded || tx.connection == nil {
		return
	}
	discardConnection(tx.connection)
	tx.discarded = true
}

func (tx *writeTx) Close() {
	if tx.closed {
		return
	}
	if tx.active {
		if err := tx.Rollback(errors.New("abandoned sqlite write")); err != nil {
			var unknown *OutcomeUnknownError
			if errors.As(err, &unknown) {
				tx.discard()
			}
		}
	}
	if !tx.discarded && tx.connection != nil {
		_ = tx.connection.Close()
	}
	tx.closed = true
	if tx.writerAdmitted {
		tx.writerAdmitted = false
		tx.store.releaseWriter()
	}
}

func (store *Store) acquireWriter(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-store.writerGate:
	}
	if err := ctx.Err(); err != nil {
		store.releaseWriter()
		return err
	}
	return nil
}

func (store *Store) releaseWriter() {
	store.writerGate <- struct{}{}
}

func discardConnection(connection *sql.Conn) {
	_ = connection.Raw(func(any) error { return sqldriver.ErrBadConn })
	_ = connection.Close()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
