package runner

import (
	"errors"
	"os"
	"time"
)

const (
	maxConfigBytes        = 256 << 10
	maxInputBytes         = 1 << 20
	maxTargetBytes        = 512 << 20
	maxFrameBytes         = 16 << 10
	maxProviderFrameBytes = maxConfigBytes

	// MaxProviderTaskBytes is the largest task body accepted before the fixed
	// shell capture framing. MaxProviderInputBytes includes that bounded
	// framing. The private control frame is sized so the complete task survives
	// JSON base64 framing without silently narrowing the Store contract.
	MaxProviderTaskBytes  = 128 << 10
	MaxProviderInputBytes = MaxProviderTaskBytes + 256

	// MaxEnvironmentEntryBytes is shared with producers of exact environment
	// entries so a value accepted before admission cannot fail only after a
	// provider launch is built.
	MaxEnvironmentEntryBytes = 8192
)

// These are the complete fixed top-level runtime names emitted by the runner.
// The daemon cleanup contract consumes the same constants; no caller may
// choose an alternate marker, spool, or scratch residue name.
const (
	OuterActivationMarkerName = "outer.activate"
	InnerActivationMarkerName = "inner.activate"
	TerminalSpoolName         = "terminal.json"
	GateConfigScratchName     = ".runner-gate.config"
	GateStdinScratchName      = ".runner-gate.stdin"
	TerminalScratchName       = ".runner-terminal.tmp"
	RuntimeLifetimeLeaseName  = ".runner-runtime.lifetime"
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
	// Control is the one fixed private duplex capability inherited by a
	// runner-owned target as descriptor 3. It is not a general ExtraFiles
	// surface and is closed before provider exec.
	Control *os.File
}

// ExecutableCommitment freezes one exact direct native executable. Its
// fields stay private so callers can carry, but cannot forge or weaken, the
// identity and content commitment made by CommitExecutableLocator.
type ExecutableCommitment struct{ executable fileCommitment }

func (ExecutableCommitment) String() string   { return "runner executable commitment (private)" }
func (ExecutableCommitment) GoString() string { return "runner.ExecutableCommitment{private}" }
func (commitment ExecutableCommitment) Path() string {
	return commitment.executable.Path
}

type LaunchSpec struct {
	commit           launchCommitment
	stdin            []byte
	stdout           *os.File
	stderr           *os.File
	control          *os.File
	controlID        *descriptorCommitment
	testFinal        *os.File // package-test-only barrier after the final pathname check
	testCurrentFinal bool     // package-test-only barrier for same-process exec
}

type descriptorCommitment struct {
	FileIdentity
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
	Mode uint32 `json:"mode"`
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
