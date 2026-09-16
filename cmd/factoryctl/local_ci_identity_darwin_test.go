//go:build darwin

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

func TestLocalCIProcessIdentity(t *testing.T) {
	identity, err := runner.ReadOwnedProcessIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	// This local read does not initialize an API client or read credentials.
	getenv := func(string) string { t.Fatal("identity helper consulted environment"); return "" }
	args := []string{"--local-ci-process-identity", strconv.Itoa(os.Getpid())}
	if code := run(context.Background(), args, getenv, &out, &diagnostics); code != 0 {
		t.Fatalf("code=%d diagnostics=%s", code, diagnostics.String())
	}
	want := fmt.Sprintf("%d:%06d %d\n", identity.Birth.Seconds, identity.Birth.Microseconds, identity.PGID)
	if out.String() != want || diagnostics.Len() != 0 {
		t.Fatalf("identity=%q diagnostics=%q", out.String(), diagnostics.String())
	}
	for _, args := range [][]string{
		{"--local-ci-process-identity"},
		{"--local-ci-process-identity", "1"},
		{"--local-ci-process-identity", "-2"},
		{"--local-ci-process-identity", "not-a-pid"},
		{"--local-ci-process-identity", "99999999999999999999999"},
		{"--local-ci-process-identity", strconv.Itoa(os.Getpid()), "--environment"},
	} {
		out.Reset()
		diagnostics.Reset()
		if code := run(context.Background(), args, getenv, &out, &diagnostics); code != exitUsage || out.Len() != 0 || diagnostics.Len() != 0 {
			t.Fatalf("malformed arguments %q: code=%d out=%q diagnostics=%q", args, code, out.String(), diagnostics.String())
		}
	}
}

func TestLocalCIProcessIdentityRefusesForeignUser(t *testing.T) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if process.Proc.P_pid <= 1 || process.Eproc.Ucred.Uid == uint32(os.Getuid()) {
			continue
		}
		var out, diagnostics bytes.Buffer
		code := run(context.Background(), []string{"--local-ci-process-identity", strconv.Itoa(int(process.Proc.P_pid))}, os.Getenv, &out, &diagnostics)
		if code != exitFailure || out.Len() != 0 || diagnostics.Len() != 0 {
			t.Fatalf("foreign user observation: code=%d out=%q diagnostics=%q", code, out.String(), diagnostics.String())
		}
		return
	}
	t.Skip("no foreign-user process available")
}
