//go:build darwin

package runner

import (
	"os"

	"golang.org/x/sys/unix"
)

func validatePrivateDirectory(dir *os.File) (fileCommitment, error) {
	if dir == nil {
		return fileCommitment{}, ErrIdentity
	}
	path, err := fdPath(dir)
	if err != nil {
		return fileCommitment{}, err
	}
	commitment, err := commitOpen(dir, path, false)
	if err != nil {
		return fileCommitment{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &stat); err != nil {
		return fileCommitment{}, err
	}
	if !validPrivateDirectoryMetadata(commitment, stat) {
		return fileCommitment{}, ErrIdentity
	}
	return commitment, nil
}

func validPrivateDirectoryMetadata(commitment fileCommitment, stat unix.Stat_t) bool {
	return commitment.FileIdentity == (FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}) &&
		commitment.UID == stat.Uid && commitment.GID == stat.Gid && commitment.Mode == uint32(stat.Mode) && commitment.Size == stat.Size &&
		commitment.MtimeSec == stat.Mtim.Sec && commitment.MtimeNsec == stat.Mtim.Nsec && commitment.CtimeSec == stat.Ctim.Sec && commitment.CtimeNsec == stat.Ctim.Nsec &&
		commitment.UID == uint32(os.Geteuid()) && commitment.Mode&uint32(unix.S_IFMT) == uint32(unix.S_IFDIR) && commitment.Mode&0o7777 == 0o700 &&
		commitment.Device != 0 && commitment.Inode != 0 && stat.Nlink != 0
}
