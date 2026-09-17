//go:build darwin

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

const (
	// takeoverDialTimeout bounds the whole handshake: connect, write the
	// grant, and read the accept/refuse reply. A live runner answers
	// immediately; anything slower is a failed handshake, and the caller
	// keeps today's RecoveredLiveHolder behaviour.
	takeoverDialTimeout = 3 * time.Second
	// maxTakeoverGrantBytes and maxTakeoverReplyBytes bound the two small
	// fixed JSON documents this handshake ever reads.
	maxTakeoverGrantBytes = 512
	maxTakeoverReplyBytes = 512
)

type takeoverGrant struct {
	RunID string `json:"run_id"`
	Token string `json:"token"`
}

type takeoverReply struct {
	Accepted bool   `json:"accepted"`
	Error    string `json:"error,omitempty"`
}

// dialTakeoverGrant reads the on-disk one-shot grant from the exact verified
// runtime directory, dials its takeover socket at the runtime's own
// diagnostic locator (a Unix domain socket has no descriptor-relative dial
// primitive), and completes the fixed accept/refuse handshake. A missing
// socket or grant file, any transport failure, a malformed reply, or an
// explicit refusal all return (nil, nil): the caller keeps today's
// RecoveredLiveHolder behaviour. Only a non-nil returned file owns a live
// daemon control capability; every other return owns nothing.
func dialTakeoverGrant(dir *os.File, locator, runID string) (*os.File, error) {
	if dir == nil || locator == "" || runID == "" {
		return nil, nil
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), runner.TakeoverSocketName, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, nil
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return nil, nil
	}
	grant, err := readTakeoverGrant(dir, runID)
	if err != nil {
		return nil, nil
	}
	dialCtx, cancel := context.WithTimeout(context.Background(), takeoverDialTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "unix", filepath.Join(locator, runner.TakeoverSocketName))
	if err != nil {
		return nil, nil
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, nil
	}
	accepted := false
	func() {
		defer func() {
			if !accepted {
				_ = unixConn.Close()
			}
		}()
		if err := unixConn.SetDeadline(time.Now().Add(takeoverDialTimeout)); err != nil {
			return
		}
		payload, err := json.Marshal(grant)
		if err != nil {
			return
		}
		// The write half stays open: this same connection carries the
		// terminal commands and the handover-quiesce this daemon sends once
		// it owns the runner.
		if _, err := unixConn.Write(append(payload, '\n')); err != nil {
			return
		}
		body, err := readTakeoverReplyLine(unixConn, maxTakeoverReplyBytes)
		if err != nil {
			return
		}
		var reply takeoverReply
		if err := json.Unmarshal(body, &reply); err != nil {
			return
		}
		accepted = reply.Accepted
	}()
	if !accepted {
		return nil, nil
	}
	file, err := unixConn.File()
	closeErr := unixConn.Close()
	if err != nil || closeErr != nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, nil
	}
	if err := file.SetDeadline(time.Time{}); err != nil {
		_ = file.Close()
		return nil, nil
	}
	return file, nil
}

// readTakeoverGrant reads the exact one-shot grant relative to the verified
// runtime directory descriptor and checks it names this exact run.
func readTakeoverGrant(dir *os.File, runID string) (takeoverGrant, error) {
	fd, err := unix.Openat(int(dir.Fd()), runner.TakeoverGrantName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return takeoverGrant{}, err
	}
	file := os.NewFile(uintptr(fd), "takeover-grant")
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxTakeoverGrantBytes+1))
	if err != nil {
		return takeoverGrant{}, err
	}
	if len(body) > maxTakeoverGrantBytes {
		return takeoverGrant{}, fmt.Errorf("daemon: takeover grant too large")
	}
	var grant takeoverGrant
	if err := json.Unmarshal(body, &grant); err != nil {
		return takeoverGrant{}, err
	}
	if grant.RunID != runID || len(grant.Token) == 0 {
		return takeoverGrant{}, errInvalidContract
	}
	return grant, nil
}

// readTakeoverReplyLine reads one newline-terminated line one byte at a time,
// so it never consumes bytes belonging to the runner protocol frames that
// follow a successful accept on the very same connection.
func readTakeoverReplyLine(conn net.Conn, limit int) ([]byte, error) {
	var line []byte
	single := make([]byte, 1)
	for len(line) < limit {
		if _, err := io.ReadFull(conn, single); err != nil {
			return nil, err
		}
		if single[0] == '\n' {
			return line, nil
		}
		line = append(line, single[0])
	}
	return nil, fmt.Errorf("daemon: takeover reply line too large")
}
