package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime/pprof"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func (daemon *Daemon) profile(ctx context.Context, call api.Call) api.Reply {
	daemon.profileMu.Lock()
	defer daemon.profileMu.Unlock()
	input, ok := call.ProfileInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	cpuPath := filepath.Join(daemon.home, "factoryd.cpu.pprof")
	heapPath := filepath.Join(daemon.home, "factoryd.heap.pprof")
	goroutinePath := filepath.Join(daemon.home, "factoryd.goroutine.pprof")
	cpu, err := os.OpenFile(cpuPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	if err = pprof.StartCPUProfile(cpu); err != nil {
		_ = cpu.Close()
		return newErrorReply(api.RemoteConflict)
	}
	timer := time.NewTimer(time.Duration(input.Seconds) * time.Second)
	select {
	case <-timer.C:
	case <-ctx.Done():
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		pprof.StopCPUProfile()
		_ = cpu.Close()
		return newErrorReply(api.RemoteUnavailable)
	}
	pprof.StopCPUProfile()
	err = errors.Join(err, cpu.Close())
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	heap, err := os.OpenFile(heapPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err == nil {
		if current := pprof.Lookup("heap"); current == nil {
			err = errors.New("heap profile unavailable")
		} else {
			err = current.WriteTo(heap, 0)
		}
		err = errors.Join(err, heap.Close())
	}
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	goroutine, err := os.OpenFile(goroutinePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err == nil {
		if current := pprof.Lookup("goroutine"); current == nil {
			err = errors.New("goroutine profile unavailable")
		} else {
			err = current.WriteTo(goroutine, 0)
		}
		err = errors.Join(err, goroutine.Close())
	}
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return api.NewContentReply(api.ProfileResult{CPUPath: cpuPath, HeapPath: heapPath, GoroutinePath: goroutinePath, Seconds: input.Seconds})
}
