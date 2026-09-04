//go:build linux

package cloudflareadmin

import "syscall"

func stableTimes(stat syscall.Stat_t) (int64, int64) {
	return stat.Mtim.Sec*1_000_000_000 + stat.Mtim.Nsec,
		stat.Ctim.Sec*1_000_000_000 + stat.Ctim.Nsec
}
