//go:build darwin

package change

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestMain turns an exact native copy of this test binary named "git" into a
// deterministic protocol fixture. The executable presented to production code
// remains a native Mach-O; the adjacent shell text is test data, not an
// executable or production authority.
func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "git" {
		plan, err := os.ReadFile(os.Args[0] + ".plan")
		if err != nil {
			os.Exit(126)
		}
		interpreter := "/bin/sh"
		arguments := append([]string{"sh", "-c", string(plan), "git"}, os.Args[1:]...)
		if bytes.HasPrefix(plan, []byte("#!/usr/bin/perl\n")) {
			interpreter = "/usr/bin/perl"
			arguments = append([]string{"perl", "-e", string(bytes.TrimPrefix(plan, []byte("#!/usr/bin/perl\n")))}, os.Args[1:]...)
		}
		if err := syscall.Exec(interpreter, arguments, os.Environ()); err != nil {
			os.Exit(127)
		}
	}
	os.Exit(m.Run())
}
