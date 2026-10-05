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

func logFactorydAt(destination io.Writer, now time.Time, format string, args ...any) {
	_, _ = fmt.Fprintf(destination, "%s %s", now.UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}
