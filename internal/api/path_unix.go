//go:build darwin || linux

package api

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
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

type tokenRecord struct {
	bearer credential
	parent fileIdentity
	file   fileIdentity
}

func (left tokenRecord) same(right tokenRecord) bool {
	return left.parent.same(right.parent) && left.file.same(right.file) && left.bearer.equal(right.bearer)
}

type socketRecord struct {
	parent fileIdentity
	socket fileIdentity
}

func (left socketRecord) same(right socketRecord) bool {
	return left.parent.same(right.parent) && left.socket.same(right.socket)
}

func validCanonicalPath(path string, maximum int) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator) && utf8.ValidString(path) && !strings.ContainsRune(path, 0) && len(path) <= maximum
}

func loadToken(path string) (tokenRecord, error) {
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
	file, err := root.Open(name)
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

func openPrivateParent(path string) (*os.Root, fileIdentity, error) {
	parentPath := filepath.Dir(path)
	if err := validateNoSymlinkComponents(parentPath); err != nil {
		return nil, fileIdentity{}, err
	}
	beforeInfo, err := os.Lstat(parentPath)
	if err != nil || !validPrivateParent(beforeInfo) {
		return nil, fileIdentity{}, ErrInvalidClient
	}
	before, ok := identityOf(beforeInfo)
	if !ok {
		return nil, fileIdentity{}, ErrInvalidClient
	}
	root, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, fileIdentity{}, ErrInvalidClient
	}
	opened, err := root.Open(".")
	if err != nil {
		root.Close()
		return nil, fileIdentity{}, ErrInvalidClient
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	openedIdentity, ok := identityOf(openedInfo)
	if statErr != nil || closeErr != nil || !ok || !openedIdentity.same(before) || !sameParent(path, before) {
		root.Close()
		return nil, fileIdentity{}, ErrInvalidClient
	}
	return root, before, nil
}

func sameParent(path string, expected fileIdentity) bool {
	info, err := os.Lstat(filepath.Dir(path))
	if err != nil || !validPrivateParent(info) {
		return false
	}
	identity, ok := identityOf(info)
	return ok && identity.same(expected)
}

func validateNoSymlinkComponents(path string) error {
	current := string(filepath.Separator)
	components := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for _, component := range components {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrInvalidClient
		}
	}
	return nil
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
