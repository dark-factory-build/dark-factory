//go:build darwin

package runner

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPrepareCommittedExecSpecReusesExactCommitmentAndBindsArgvZero(t *testing.T) {
	executable, err := CommitExecutableLocator("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	for _, argv := range [][]string{nil, {}, {""}, {"/bin/false", "-s"}} {
		if _, err := PrepareCommittedExecSpec(executable, argv, []string{"PATH=/usr/bin:/bin"}, cwd); !errors.Is(err, ErrIdentity) {
			t.Fatalf("argv=%q error=%v, want ErrIdentity", argv, err)
		}
	}

	spec, err := PrepareCommittedExecSpec(executable, []string{"/bin/sh", "-s"}, []string{"PATH=/usr/bin:/bin"}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if spec.commit.Executable != executable.executable {
		t.Fatal("prepared launch did not retain the exact executable commitment")
	}
	if spec.commit.Argv[0] != executable.Path() {
		t.Fatalf("argv[0]=%q, want %q", spec.commit.Argv[0], executable.Path())
	}
}

func TestPrepareCommittedExecSpecCopiesArgvAndEnvironment(t *testing.T) {
	executable, err := CommitExecutableLocator("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	argv := []string{"/bin/sh", "-s"}
	environment := []string{"PATH=/usr/bin:/bin", "LANG=C"}
	spec, err := PrepareCommittedExecSpec(executable, argv, environment, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	argv[1] = "-c"
	environment[0] = "PATH=/private/ambient"
	if !slices.Equal(spec.commit.Argv, []string{"/bin/sh", "-s"}) {
		t.Fatalf("prepared argv changed with caller: %q", spec.commit.Argv)
	}
	if !slices.Equal(spec.commit.Env, []string{"PATH=/usr/bin:/bin", "LANG=C"}) {
		t.Fatalf("prepared environment changed with caller: %q", spec.commit.Env)
	}
}

func TestPrepareExecSpecsRejectInvalidUTF8AndEmptyArgvBeforeCommitment(t *testing.T) {
	invalid := string([]byte{0xff})
	for _, test := range []struct {
		name string
		argv []string
	}{
		{name: "invalid UTF-8", argv: []string{"/bin/sh", invalid}},
		{name: "empty argument", argv: []string{"/bin/sh", ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := PrepareExecSpec(ExecSpec{Target: test.argv[0], Args: test.argv[1:], Cwd: t.TempDir()})
			if err == nil || !strings.Contains(err.Error(), "invalid argv") {
				t.Fatalf("PrepareExecSpec error=%v, want invalid argv before commitment", err)
			}
			executable, err := CommitExecutableLocator("/bin/sh")
			if err != nil {
				t.Fatal(err)
			}
			_, err = PrepareCommittedExecSpec(executable, test.argv, []string{"PATH=/usr/bin:/bin"}, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), "invalid argv") {
				t.Fatalf("PrepareCommittedExecSpec error=%v, want invalid argv before commitment", err)
			}
		})
	}
}

func TestPrepareCommittedExecSpecPreservesValidUnicodeArgvBytes(t *testing.T) {
	executable, err := CommitExecutableLocator("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/sh", "-c", "printf '%s' \"$1\"", "sh", "工場"}
	spec, err := PrepareCommittedExecSpec(executable, want, []string{"PATH=/usr/bin:/bin"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.commit.Argv, want) {
		t.Fatalf("argv bytes changed: got=%q want=%q", spec.commit.Argv, want)
	}
}

func TestPrepareExecSpecPreservesValidUnicodeArgvBytes(t *testing.T) {
	want := []string{"/bin/sh", "工場"}
	spec, err := PrepareExecSpec(ExecSpec{Target: want[0], Args: want[1:], Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.commit.Argv, want) {
		t.Fatalf("argv bytes changed: got=%q want=%q", spec.commit.Argv, want)
	}
}
