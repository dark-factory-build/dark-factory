//go:build darwin

package runner

import (
	"errors"
	"slices"
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
