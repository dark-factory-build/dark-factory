package runner

import (
	"errors"
	"os"
	"time"
)

const (
	maxConfigBytes = 256 << 10
	maxInputBytes  = 1 << 20
	maxTargetBytes = 512 << 20
	maxFrameBytes  = 16 << 10
)

var (
	ErrUnsupported = errors.New("runner: unsupported platform")
	ErrState       = errors.New("runner: invalid lifecycle state")
	ErrIdentity    = errors.New("runner: identity mismatch")
	ErrUnresolved  = errors.New("runner: process state unresolved")
	ErrConflict    = errors.New("runner: durable record conflict")
)

type Birth struct {
	Seconds      int64 `json:"seconds"`
	Microseconds int32 `json:"microseconds"`
}

type Identity struct {
	PID   int   `json:"pid"`
	PGID  int   `json:"pgid"`
	Birth Birth `json:"birth"`
}

func (i Identity) Valid() bool {
	return i.PID > 1 && i.PGID > 1 && i.Birth.Seconds > 0 && i.Birth.Microseconds >= 0 && i.Birth.Microseconds < 1_000_000
}

type FileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type Presence string

const (
	Present Presence = "present"
	Absent  Presence = "absent"
	Reused  Presence = "reused"
	Unknown Presence = "unknown"
)

type Observation struct {
	Presence Presence
	Members  []Identity
	Err      error
}

type Exit struct {
	Code      int    `json:"code"`
	Signal    int    `json:"signal"`
	Aborted   bool   `json:"aborted"`
	LaunchErr string `json:"launch_error,omitempty"`
}

type Terminal struct {
	AttemptID string   `json:"attempt_id"`
	Process   Identity `json:"process"`
	Exit      Exit     `json:"exit"`
	Message   string   `json:"message,omitempty"`
}

type ExecSpec struct {
	Target string
	Args   []string
	Env    []string
	Cwd    string
	Stdin  []byte
	Stdout *os.File
	Stderr *os.File
}

type LaunchSpec struct {
	commit    launchCommitment
	stdin     []byte
	stdout    *os.File
	stderr    *os.File
	testFinal *os.File // package-test-only barrier after the final pathname check
}

type fileCommitment struct {
	Path string `json:"path"`
	FileIdentity
	UID       uint32 `json:"uid"`
	GID       uint32 `json:"gid"`
	Mode      uint32 `json:"mode"`
	Size      int64  `json:"size"`
	MtimeSec  int64  `json:"mtime_sec"`
	MtimeNsec int64  `json:"mtime_nsec"`
	CtimeSec  int64  `json:"ctime_sec"`
	CtimeNsec int64  `json:"ctime_nsec"`
	SHA256    string `json:"sha256,omitempty"`
}

type launchCommitment struct {
	Executable fileCommitment `json:"executable"`
	Cwd        fileCommitment `json:"cwd"`
	Argv       []string       `json:"argv"`
	Env        []string       `json:"env"`
}

type state uint8

const (
	stateBlocked state = iota + 1
	stateActivated
	stateExited
	stateWaited
)

const defaultStopTimeout = 750 * time.Millisecond
