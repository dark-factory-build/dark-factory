//go:build darwin || linux

package api

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const maxSocketPathBytes = 103

type fileIdentity struct {
	device, inode uint64
	uid           uint32
	mode          os.FileMode
	links         uint64
	size          int64
	modifiedNS    int64
}

func (left fileIdentity) same(right fileIdentity) bool { return left == right }

func (left fileIdentity) sameObject(right fileIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.uid == right.uid && left.links == right.links && left.size == right.size
}

func (left fileIdentity) sameDirectory(right fileIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.uid == right.uid && left.mode == right.mode
}

type tokenRecord struct {
	bearer credential
	parent fileIdentity
	file   fileIdentity
}

func (left tokenRecord) same(right tokenRecord) bool {
	return left.parent.sameDirectory(right.parent) && left.file.same(right.file) && left.bearer.equal(right.bearer)
}

type socketRecord struct {
	parent fileIdentity
	socket fileIdentity
}

type privateRoot struct {
	root      *os.Root
	directory *os.File
}

func (root *privateRoot) Close() error {
	rootErr := root.root.Close()
	directoryErr := root.directory.Close()
	if rootErr != nil {
		return rootErr
	}
	return directoryErr
}

func (root *privateRoot) Lstat(name string) (os.FileInfo, error) {
	return root.root.Lstat(name)
}

func (root *privateRoot) openToken(name string) (*os.File, error) {
	fd, err := unix.Openat(int(root.directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (left socketRecord) same(right socketRecord) bool {
	return left.parent.sameDirectory(right.parent) && left.socket.same(right.socket)
}

func validCanonicalPath(path string, maximum int) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator) && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && len(path) <= maximum
}

func loadToken(path string) (tokenRecord, error) {
	return loadTokenAtOpen(path, nil)
}

// loadTokenAtOpen keeps the only token-open race seam at the exact boundary
// between the descriptor-relative metadata check and open. Production callers
// pass nil; tests use it to prove replacement cannot block or change identity.
func loadTokenAtOpen(path string, beforeOpen func()) (tokenRecord, error) {
	if !validCanonicalPath(path, 4096) {
		return tokenRecord{}, ErrInvalidClient
	}
	root, parent, err := openPrivateParent(path)
	if err != nil {
		return tokenRecord{}, err
	}
	defer root.Close()
	name := filepath.Base(path)
	beforeInfo, err := root.Lstat(name)
	if err != nil || !validTokenInfo(beforeInfo) {
		return tokenRecord{}, ErrInvalidClient
	}
	before, ok := identityOf(beforeInfo)
	if !ok {
		return tokenRecord{}, ErrInvalidClient
	}
	if beforeOpen != nil {
		beforeOpen()
	}
	file, err := root.openToken(name)
	if err != nil {
		return tokenRecord{}, ErrInvalidClient
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !validTokenInfo(openedInfo) {
		return tokenRecord{}, ErrInvalidClient
	}
	opened, ok := identityOf(openedInfo)
	if !ok || !opened.same(before) {
		return tokenRecord{}, ErrInvalidClient
	}
	contents, err := io.ReadAll(io.LimitReader(file, credentialBytes+1))
	if err != nil || len(contents) != credentialBytes {
		return tokenRecord{}, ErrInvalidClient
	}
	afterOpenInfo, err := file.Stat()
	if err != nil {
		return tokenRecord{}, ErrInvalidClient
	}
	afterOpen, ok := identityOf(afterOpenInfo)
	if !ok || !afterOpen.same(before) {
		return tokenRecord{}, ErrInvalidClient
	}
	afterInfo, err := root.Lstat(name)
	if err != nil {
		return tokenRecord{}, ErrInvalidClient
	}
	after, ok := identityOf(afterInfo)
	if !ok || !after.same(before) {
		return tokenRecord{}, ErrInvalidClient
	}
	absoluteInfo, err := os.Lstat(path)
	if err != nil {
		return tokenRecord{}, ErrInvalidClient
	}
	absolute, ok := identityOf(absoluteInfo)
	if !ok || !absolute.same(before) || !sameParent(path, parent) {
		return tokenRecord{}, ErrInvalidClient
	}
	var bearer credential
	copy(bearer[:], contents)
	return tokenRecord{bearer: bearer, parent: parent, file: before}, nil
}

func inspectSocket(path string) (socketRecord, error) {
	if !validCanonicalPath(path, maxSocketPathBytes) {
		return socketRecord{}, ErrInvalidClient
	}
	root, parent, err := openPrivateParent(path)
	if err != nil {
		return socketRecord{}, err
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Base(path))
	if err != nil || !validSocketInfo(info) {
		return socketRecord{}, ErrInvalidClient
	}
	identity, ok := identityOf(info)
	if !ok {
		return socketRecord{}, ErrInvalidClient
	}
	absoluteInfo, err := os.Lstat(path)
	if err != nil {
		return socketRecord{}, ErrInvalidClient
	}
	absolute, ok := identityOf(absoluteInfo)
	if !ok || !absolute.same(identity) || !sameParent(path, parent) {
		return socketRecord{}, ErrInvalidClient
	}
	return socketRecord{parent: parent, socket: identity}, nil
}

func openPrivateParent(path string) (*privateRoot, fileIdentity, error) {
	return openPrivateParentAt(path, nil, nil)
}

// openPrivateParentAt walks each canonical component from an open root. Each
// directory is opened relative to its verified parent, and its identity and
// non-symlink type must agree before, through, and after the open.
func openPrivateParentAt(path string, beforeOpen, afterOpen func(string)) (*privateRoot, fileIdentity, error) {
	parentPath := filepath.Dir(path)
	if !validCanonicalPath(path, 4096) || parentPath == string(filepath.Separator) {
		return nil, fileIdentity{}, ErrInvalidClient
	}
	directory, identity, err := openParentChain(parentPath, beforeOpen, afterOpen)
	if err != nil {
		return nil, fileIdentity{}, ErrInvalidClient
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		directory.Close()
		return nil, fileIdentity{}, ErrInvalidClient
	}
	opened, err := root.Open(".")
	if err != nil {
		root.Close()
		directory.Close()
		return nil, fileIdentity{}, ErrInvalidClient
	}
	info, statErr := opened.Stat()
	closeErr := opened.Close()
	rootIdentity, ok := identityOf(info)
	if statErr != nil || closeErr != nil || !ok || !rootIdentity.same(identity) {
		root.Close()
		directory.Close()
		return nil, fileIdentity{}, ErrInvalidClient
	}
	check, checkedIdentity, err := openParentChain(parentPath, nil, nil)
	if err != nil || check.Close() != nil || !checkedIdentity.same(identity) {
		root.Close()
		directory.Close()
		return nil, fileIdentity{}, ErrInvalidClient
	}
	return &privateRoot{root: root, directory: directory}, identity, nil
}

func openParentChain(parentPath string, beforeOpen, afterOpen func(string)) (*os.File, fileIdentity, error) {
	directory, err := os.Open(string(filepath.Separator))
	if err != nil {
		return nil, fileIdentity{}, ErrInvalidClient
	}
	currentPath := string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(parentPath, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		currentPath = filepath.Join(currentPath, component)
		beforeInfo, statErr := os.Lstat(currentPath)
		before, ok := identityOf(beforeInfo)
		if statErr != nil || !ok || !validDirectoryComponent(beforeInfo) {
			directory.Close()
			return nil, fileIdentity{}, ErrInvalidClient
		}
		if beforeOpen != nil {
			beforeOpen(currentPath)
		}
		fd, openErr := unix.Openat(int(directory.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if openErr != nil {
			directory.Close()
			return nil, fileIdentity{}, ErrInvalidClient
		}
		next := os.NewFile(uintptr(fd), currentPath)
		if afterOpen != nil {
			afterOpen(currentPath)
		}
		openedInfo, openedErr := next.Stat()
		openedIdentity, openedOK := identityOf(openedInfo)
		afterInfo, afterErr := os.Lstat(currentPath)
		afterIdentity, afterOK := identityOf(afterInfo)
		if openedErr != nil || !openedOK || !openedIdentity.same(before) || afterErr != nil || !afterOK ||
			!validDirectoryComponent(afterInfo) || !afterIdentity.same(before) {
			next.Close()
			directory.Close()
			return nil, fileIdentity{}, ErrInvalidClient
		}
		if err := directory.Close(); err != nil {
			next.Close()
			return nil, fileIdentity{}, ErrInvalidClient
		}
		directory = next
	}
	info, err := directory.Stat()
	identity, ok := identityOf(info)
	if err != nil || !ok || !validPrivateParent(info) {
		directory.Close()
		return nil, fileIdentity{}, ErrInvalidClient
	}
	return directory, identity, nil
}

func sameParent(path string, expected fileIdentity) bool {
	root, actual, err := openPrivateParent(path)
	if err != nil {
		return false
	}
	return root.Close() == nil && actual.same(expected)
}

func validDirectoryComponent(info os.FileInfo) bool {
	_, ok := identityOf(info)
	return ok && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func validPrivateParent(info os.FileInfo) bool {
	identity, ok := identityOf(info)
	return ok && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o700 && identity.uid == uint32(os.Geteuid())
}

func validTokenInfo(info os.FileInfo) bool {
	identity, ok := identityOf(info)
	return ok && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm() == 0o600 && identity.uid == uint32(os.Geteuid()) && identity.links == 1 && identity.size == credentialBytes
}

func validSocketInfo(info os.FileInfo) bool {
	identity, ok := identityOf(info)
	return ok && info.Mode()&os.ModeSocket != 0 && info.Mode().Perm() == 0o600 && identity.uid == uint32(os.Geteuid()) && identity.links == 1 && identity.size == 0
}

func identityOf(info os.FileInfo) (fileIdentity, bool) {
	if info == nil {
		return fileIdentity{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, false
	}
	return fileIdentity{
		device: uint64(stat.Dev), inode: uint64(stat.Ino), uid: stat.Uid,
		mode: info.Mode(), links: uint64(stat.Nlink), size: stat.Size, modifiedNS: info.ModTime().UnixNano(),
	}, true
}
