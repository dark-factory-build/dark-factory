package daemon

import (
	"fmt"
	"io"
	"time"
)

// LogFactoryd is the one stderr boundary for factoryd diagnostics. Callers
// supply the existing message, including its factoryd: prefix, unchanged.
func LogFactoryd(destination io.Writer, format string, args ...any) {
	logFactorydAt(destination, time.Now(), format, args...)
}

// timeMaintenance runs one hourly maintenance step and names it when it took
// over a second: the steps share the store writer. Only run is timed.
func timeMaintenance(destination io.Writer, step string, run func()) {
	started := time.Now()
	run()
	if took := time.Since(started); took > time.Second {
		LogFactoryd(destination, "factoryd: hourly maintenance %s took %s\n", step, took.Round(time.Millisecond))
	}
}

func logFactorydAt(destination io.Writer, now time.Time, format string, args ...any) {
	_, _ = fmt.Fprintf(destination, "%s %s", now.UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
