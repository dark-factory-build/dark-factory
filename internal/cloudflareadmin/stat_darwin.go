//go:build darwin

package cloudflareadmin

import "syscall"

func stableTimes(stat syscall.Stat_t) (int64, int64) {
	return stat.Mtimespec.Sec*1_000_000_000 + stat.Mtimespec.Nsec,
		stat.Ctimespec.Sec*1_000_000_000 + stat.Ctimespec.Nsec
}
