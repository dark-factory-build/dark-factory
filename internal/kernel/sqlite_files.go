package kernel

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	maxSQLiteWALSize   = 272 << 20
	maxSQLiteSHMSize   = 4 << 20
	walHeaderSize      = 32
	walFormatVersion   = 3007000
	walIndexRegionSize = 32768
)

func Open(ctx context.Context, absolutePath string) (*Store, error) {
	if err := validateDatabasePath(absolutePath); err != nil {
		return nil, err
	}
	files, err := openDatabaseFiles(absolutePath)
	if err != nil {
		return nil, err
	}
	for {
		if err := files.refreshPinnedInfo(); err != nil {
			return nil, errors.Join(err, files.Close())
		}
		snapshot, err := preflightExisting(ctx, files)
		if errors.Is(err, errDatabaseSnapshotChanged) {
			continue
		}
		if err != nil {
			return nil, errors.Join(err, files.Close())
		}
		if err := files.verifySnapshot(ctx, snapshot); errors.Is(err, errDatabaseSnapshotChanged) && files.wal != nil {
			continue
		} else if err != nil {
			return nil, errors.Join(err, files.Close())
		}
		if err := files.recheckPaths(); err != nil {
			return nil, errors.Join(err, files.Close())
		}
		break
	}
	if files.wal == nil {
		if err := files.reservePrivateSHM(); err != nil {
			return nil, errors.Join(err, files.Close())
		}
	}
	store, err := openPools(absolutePath)
	if err != nil {
		return nil, errors.Join(err, files.removeEmptyCreatedSidecars(), files.Close())
	}
	if files.wal == nil {
		files.wal, err = files.openDatabaseFile(files.main.name+"-wal", "WAL", 0, maxSQLiteWALSize)
		if err != nil {
			return nil, closeRejectedOpen(store, files, err)
		}
	}
	if err := store.validateOpen(ctx); err != nil {
		return nil, closeRejectedOpen(store, files, err)
	}
	if err := files.recheckPaths(); err != nil {
		return nil, closeRejectedOpen(store, files, err)
	}
	if err := files.Close(); err != nil {
		return nil, errors.Join(err, store.Close())
	}
	return store, nil
}

func closeRejectedOpen(store *Store, files *databaseFiles, cause error) error {
	return errors.Join(cause, store.Close(), files.Close())
}

func validateDatabasePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: sqlite path must be absolute: %q", ErrInvalidValue, path)
	}
	return nil
}

func preflightExisting(ctx context.Context, files *databaseFiles) (databaseSnapshot, error) {
	if files.wal == nil {
		header := make([]byte, 20)
		if _, err := files.main.file.ReadAt(header, 0); err != nil {
			return databaseSnapshot{}, fmt.Errorf("%w: read sqlite header: %w", ErrForeignDatabase, err)
		}
		if err := validateJournalHeaderBytes(header, false); err != nil {
			return databaseSnapshot{}, err
		}
		digest, err := digestDatabaseFile(ctx, files.main)
		if err != nil {
			return databaseSnapshot{}, err
		}
		return databaseSnapshot{main: digest}, inspectImmutable(ctx, files.main.file, files.main.info.Size(), header[18] == 1)
	}
	return validateWALSnapshotCopy(ctx, files)
}

var errDatabaseSnapshotChanged = errors.New("sqlite database changed during preflight")

type databaseDigest struct {
	size int64
	sum  [sha256.Size]byte
}

type databaseSnapshot struct {
	main databaseDigest
	wal  databaseDigest
	shm  databaseDigest
}

type databaseFile struct {
	name    string
	file    *os.File
	info    os.FileInfo
	stat    unix.Stat_t
	minimum int64
	maximum int64
}

type databaseFiles struct {
	directoryPath string
	directory     *os.File
	directoryStat unix.Stat_t
	main          *databaseFile
	wal           *databaseFile
	shm           *databaseFile
	reservedSHM   *databaseFile
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
	file := os.NewFile(uintptr(fd), filepath.Join(files.directoryPath, name))
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
	return &databaseFile{name: name, file: file, info: info, stat: stat, minimum: minimum, maximum: maximum}, nil
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

func (files *databaseFiles) refreshPinnedInfo() error {
	for _, source := range []*databaseFile{files.main, files.wal, files.shm} {
		if source == nil {
			continue
		}
		current, err := source.file.Stat()
		if err != nil {
			return fmt.Errorf("refresh pinned sqlite file: %w", err)
		}
		if !os.SameFile(source.info, current) {
			return fmt.Errorf("%w: pinned sqlite file identity changed", ErrCorruptState)
		}
		if err := validateDatabaseFileInfo(current, source.name, source.minimum, source.maximum); err != nil {
			return err
		}
		source.info = current
	}
	if err := validateMainFile(files.main, files.wal != nil); err != nil {
		return err
	}
	if files.wal == nil {
		return nil
	}
	if files.shm.info.Size()%walIndexRegionSize != 0 {
		return fmt.Errorf("%w: SHM size %d is not a positive multiple of %d", ErrCorruptState, files.shm.info.Size(), walIndexRegionSize)
	}
	pageSize, err := databasePageSize(files.main.file)
	if err != nil {
		return err
	}
	return validateWAL(files.wal.file, files.wal.info.Size(), pageSize)
}

func digestDatabaseFile(ctx context.Context, source *databaseFile) (databaseDigest, error) {
	result := databaseDigest{size: source.info.Size()}
	hash := sha256.New()
	const bufferSize = 128 << 10
	buffer := make([]byte, bufferSize)
	for offset := int64(0); offset < result.size; {
		if err := ctx.Err(); err != nil {
			return databaseDigest{}, err
		}
		want := int64(len(buffer))
		if remaining := result.size - offset; want > remaining {
			want = remaining
		}
		read, err := source.file.ReadAt(buffer[:int(want)], offset)
		if read != int(want) {
			return databaseDigest{}, fmt.Errorf("digest sqlite %s at %d: %w", source.name, offset, errors.Join(io.ErrUnexpectedEOF, err))
		}
		if err != nil {
			return databaseDigest{}, fmt.Errorf("digest sqlite %s at %d: %w", source.name, offset, err)
		}
		if _, err := hash.Write(buffer[:read]); err != nil {
			return databaseDigest{}, fmt.Errorf("digest sqlite %s: %w", source.name, err)
		}
		offset += int64(read)
	}
	copy(result.sum[:], hash.Sum(nil))
	return result, nil
}

func (files *databaseFiles) verifySnapshot(ctx context.Context, expected databaseSnapshot) error {
	for _, item := range []struct {
		source *databaseFile
		digest databaseDigest
	}{{files.main, expected.main}, {files.wal, expected.wal}, {files.shm, expected.shm}} {
		if item.source == nil {
			continue
		}
		current, err := item.source.file.Stat()
		if err != nil {
			return fmt.Errorf("recheck sqlite snapshot size: %w", err)
		}
		if current.Size() != item.digest.size {
			return fmt.Errorf("%w: sqlite %s length changed", errDatabaseSnapshotChanged, item.source.name)
		}
		item.source.info = current
		actual, err := digestDatabaseFile(ctx, item.source)
		if err != nil {
			return err
		}
		if actual != item.digest {
			return fmt.Errorf("%w: sqlite %s content changed", errDatabaseSnapshotChanged, item.source.name)
		}
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
	return pathPresentAt(int(files.directory.Fd()), name)
}

func pathPresentAt(directoryFD int, name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
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
		if err := validateDatabaseFileInfo(current, source.name, source.minimum, source.maximum); err != nil {
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
	files := &databaseFiles{directoryPath: directoryPath, directory: directory, main: &databaseFile{name: filepath.Base(path)}}
	if err := unix.Fstat(fd, &files.directoryStat); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect sqlite parent directory: %w", err)
	}
	return files, nil
}

func (files *databaseFiles) removeEmptyCreatedSidecars() error {
	if files == nil || files.wal != nil {
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
	files.reservedSHM = &databaseFile{name: name, file: file, info: info, stat: stat, minimum: 0, maximum: maxSQLiteSHMSize}
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

func validateWALSnapshotCopy(ctx context.Context, sources *databaseFiles) (snapshot databaseSnapshot, resultErr error) {
	directory, err := os.MkdirTemp("", "dark-factory-wal-preflight-")
	if err != nil {
		return databaseSnapshot{}, fmt.Errorf("create private WAL preflight directory: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, os.RemoveAll(directory))
	}()
	if err := os.Chmod(directory, 0o700); err != nil {
		return databaseSnapshot{}, fmt.Errorf("secure WAL preflight directory: %w", err)
	}
	if err := validatePrivateDirectory(directory); err != nil {
		return databaseSnapshot{}, err
	}
	temporaryMain := filepath.Join(directory, "factory.sqlite3")
	snapshot.main, err = copyDatabaseFile(ctx, sources.main, temporaryMain)
	if err != nil {
		return databaseSnapshot{}, err
	}
	snapshot.wal, err = copyDatabaseFile(ctx, sources.wal, temporaryMain+"-wal")
	if err != nil {
		return databaseSnapshot{}, err
	}
	snapshot.shm, err = digestDatabaseFile(ctx, sources.shm)
	if err != nil {
		return databaseSnapshot{}, err
	}

	pool, err := sql.Open(driverName, walPreflightDataSource(temporaryMain))
	if err != nil {
		return databaseSnapshot{}, fmt.Errorf("open isolated WAL snapshot: %w", err)
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
		return databaseSnapshot{}, fmt.Errorf("checkout isolated WAL snapshot: %w", err)
	}
	tx, err := beginPinnedRead(ctx, connection)
	if err != nil {
		return databaseSnapshot{}, errors.Join(fmt.Errorf("begin isolated WAL snapshot: %w", err), connection.Close())
	}
	var journalMode string
	err = tx.connection.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode)
	if err == nil && strings.ToLower(journalMode) != "wal" {
		err = fmt.Errorf("%w: isolated database did not recover in WAL mode", ErrCorruptState)
	}
	if err == nil {
		err = validateDatabaseSnapshot(ctx, tx.connection)
	}
	validationErr := errors.Join(err, tx.Close())
	if err := sources.verifySnapshot(ctx, snapshot); err != nil {
		return databaseSnapshot{}, err
	}
	return snapshot, validationErr
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

func copyDatabaseFile(ctx context.Context, source *databaseFile, targetPath string) (digest databaseDigest, resultErr error) {
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return databaseDigest{}, fmt.Errorf("create isolated sqlite copy: %w", err)
	}
	defer func() {
		if err := target.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close isolated sqlite copy: %w", err))
		}
	}()
	digest.size = source.info.Size()
	hash := sha256.New()
	const bufferSize = 128 << 10
	buffer := make([]byte, bufferSize)
	section := io.NewSectionReader(source.file, 0, source.info.Size())
	for {
		if err := ctx.Err(); err != nil {
			return databaseDigest{}, err
		}
		read, readErr := section.Read(buffer)
		if read > 0 {
			written, writeErr := target.Write(buffer[:read])
			if writeErr != nil {
				return databaseDigest{}, fmt.Errorf("write isolated sqlite copy: %w", writeErr)
			}
			if written != read {
				return databaseDigest{}, fmt.Errorf("write isolated sqlite copy: %w", io.ErrShortWrite)
			}
			if _, err := hash.Write(buffer[:read]); err != nil {
				return databaseDigest{}, fmt.Errorf("digest isolated sqlite copy: %w", err)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return databaseDigest{}, fmt.Errorf("read sqlite source for isolated copy: %w", readErr)
		}
	}
	copy(digest.sum[:], hash.Sum(nil))
	return digest, nil
}
