package runner

import (
	"encoding/json"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// AttemptDiagnosticsName is the runner's account of how an attempt ended:
// the error that ended it, if any, and the terminal output retained up to the
// end. It is written before the attempt result, so the daemon reads it from
// disk instead of depending on terminal credit or on which of a worker's
// refusal and its exit the runner observed first.
const (
	AttemptDiagnosticsName     = "attempt-diagnostics.json"
	MaxAttemptDiagnosticsBytes = 2 << 20
)

type AttemptDiagnostics struct {
	Cause  string `json:"cause,omitempty"`
	Floor  uint64 `json:"floor"`
	Head   uint64 `json:"head"`
	Output []byte `json:"output,omitempty"`
}

func writeAttemptDiagnostics(dir *os.File, cause error, ring *terminalByteRing) error {
	var value AttemptDiagnostics
	if cause != nil {
		value.Cause = cause.Error()[:min(len(cause.Error()), maxProviderErrorBytes)]
	}
	if ring != nil {
		value.Floor, value.Head = ring.Floor(), ring.Head()
		for cursor := value.Floor; cursor < value.Head; {
			chunk, next, err := ring.Read(cursor)
			if err != nil {
				return err
			}
			value.Output, cursor = append(value.Output, chunk...), next
		}
	}
	if value.Cause == "" && len(value.Output) == 0 {
		return nil
	}
	body, err := json.Marshal(value)
	if err != nil || len(body) > MaxAttemptDiagnosticsBytes {
		return errors.Join(ErrIdentity, err)
	}
	fd, err := unix.Openat(int(dir.Fd()), AttemptDiagnosticsName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	return errors.Join(writeAll(fd, body), unix.Close(fd))
}

// ReadAttemptDiagnostics returns what the runner recorded, if anything. Its
// content is the provider's own output: callers treat it as untrusted text.
func ReadAttemptDiagnostics(dir *os.File) (AttemptDiagnostics, bool, error) {
	fd, err := unix.Openat(int(dir.Fd()), AttemptDiagnosticsName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return AttemptDiagnostics{}, false, nil
	}
	if err != nil {
		return AttemptDiagnostics{}, false, err
	}
	file := os.NewFile(uintptr(fd), AttemptDiagnosticsName)
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, MaxAttemptDiagnosticsBytes+1))
	var value AttemptDiagnostics
	if err == nil && len(body) <= MaxAttemptDiagnosticsBytes {
		err = json.Unmarshal(body, &value)
	}
	if err != nil || len(body) > MaxAttemptDiagnosticsBytes || value.Head < value.Floor || uint64(len(value.Output)) > value.Head-value.Floor {
		return AttemptDiagnostics{}, false, errors.Join(ErrIdentity, err)
	}
	return value, true, nil
}
