//go:build darwin

package change

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"golang.org/x/sys/unix"
)

type localGitFixture struct {
	git        string
	repository string
	identity   RepositoryIdentity
	format     ObjectFormat
	base       ObjectID
	files      []fixtureFile
}

type fixtureFile struct {
	path []byte
	mode string
	data []byte
}

func TestContentWriteGitArgumentsForceDurabilityAndDisableHooks(t *testing.T) {
	want := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsync=all", "-c", "core.fsyncMethod=fsync", "-C", "/repository", "update-ref", "ref", "new", "old"}
	got := contentWriteGitArguments("/repository", "update-ref", "ref", "new", "old")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("content write arguments = %q, want %q", got, want)
	}
}

func TestSelectGitRealSHA1AndSHA256WithoutBlobReads(t *testing.T) {
	for _, formatName := range []string{"sha1", "sha256"} {
		t.Run(formatName, func(t *testing.T) {
			fixture := newLocalGitFixture(t, formatName)
			selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
			if err != nil {
				t.Fatal(err)
			}
			if !selected.RepositoryIdentity().Equal(fixture.identity) || selected.ObjectFormat() != fixture.format || !selected.Base().equal(fixture.base) {
				t.Fatalf("selection differs: %+v", selected)
			}
		})
	}
}

func TestSelectGitRepositoryShapesAndReplacement(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	alias := fixture.repository + "/../" + filepath.Base(fixture.repository)
	if _, err := SelectGit(context.Background(), fixture.git, alias, "HEAD", fixture.identity); err == nil {
		t.Fatal(".. repository alias accepted")
	}
	symlinkRoot := filepath.Join(filepath.Dir(fixture.repository), "repository-link")
	if err := os.Symlink(fixture.repository, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectGit(context.Background(), fixture.git, symlinkRoot, "HEAD", fixture.identity); err == nil {
		t.Fatal("symlink repository root accepted")
	}

	parentRepository := secureTempDir(t)
	runFixtureGit(t, fixture.git, parentRepository, "init")
	nested := filepath.Join(parentRepository, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	nestedIdentity := mustRepositoryIdentity(t, nested)
	if _, err := SelectGit(context.Background(), fixture.git, nested, "HEAD", nestedIdentity); err == nil {
		t.Fatal("upward repository discovery accepted")
	}

	bare := filepath.Join(secureTempDir(t), "bare.git")
	runFixtureGit(t, fixture.git, filepath.Dir(bare), "init", "--bare", bare)
	if _, err := SelectGit(context.Background(), fixture.git, bare, "HEAD", mustRepositoryIdentity(t, bare)); err == nil {
		t.Fatal("bare repository accepted")
	}

	linked := filepath.Join(filepath.Dir(fixture.repository), "linked")
	runFixtureGit(t, fixture.git, fixture.repository, "worktree", "add", "-b", "linked-test", linked, "HEAD")
	if _, err := SelectGit(context.Background(), fixture.git, linked, "HEAD", mustRepositoryIdentity(t, linked)); err == nil {
		t.Fatal("linked worktree .git file accepted")
	}

	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	original := fixture.repository + "-original"
	if err := os.Rename(fixture.repository, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fixture.repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := AddWorktree(context.Background(), selected, filepath.Join(secureTempDir(t), "published"), BranchName("0123456789abcdef0123456789abcdef")); err == nil {
		t.Fatal("replacement repository served an old selection")
	}
}

func TestVerifyRepositoryRootIgnoresGitAdministration(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	gitDirectory := filepath.Join(fixture.repository, ".git")
	if err := os.Rename(gitDirectory, gitDirectory+".retained"); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRepositoryRoot(fixture.repository, fixture.identity); err != nil {
		t.Fatalf("root-only verification required Git administration: %v", err)
	}
	if err := os.Chmod(fixture.repository, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRepositoryRoot(fixture.repository, fixture.identity); err == nil {
		t.Fatal("unsafe repository root mode accepted")
	}
}

func TestSelectGitRejectsPartialCloneAlternatesAndNonCommitRevision(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "core.repositoryformatversion", "1")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "extensions.partialClone", "origin")
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil {
		t.Fatal("partial clone accepted")
	}
	runFixtureGit(t, fixture.git, fixture.repository, "config", "--unset", "extensions.partialClone")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "remote.origin.promisor", "true")
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil {
		t.Fatal("direct promisor remote accepted")
	}
	runFixtureGit(t, fixture.git, fixture.repository, "config", "--unset", "remote.origin.promisor")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "core.repositoryformatversion", "1")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "extensions.worktreeConfig", "true")
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err != nil {
		t.Fatalf("worktreeConfig without config.worktree was rejected: %v", err)
	}
	alternates := filepath.Join(fixture.repository, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte("/private/tmp/objects\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil {
		t.Fatal("object alternates accepted")
	}
	if err := os.Remove(alternates); err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "hash-object", "README.md"))
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, blob, fixture.identity); err == nil {
		t.Fatal("non-commit revision accepted")
	}
	runFixtureGit(t, fixture.git, fixture.repository, "tag", "-a", "-m", "fixture tag", "release", "HEAD")
	tagged, err := SelectGit(context.Background(), fixture.git, fixture.repository, "release", fixture.identity)
	if err != nil {
		t.Fatalf("annotated tag peeling to a commit was refused: %v", err)
	}
	if !tagged.Base().equal(fixture.base) {
		t.Fatalf("tag selected %s, want commit %s", tagged.Base().Hex(), fixture.base.Hex())
	}
}

func TestSelectGitRejectsLocalIncludesBeforeExecutingGit(t *testing.T) {
	for name, section := range map[string]string{
		"direct include":      "[include]\n\tpath = ../outside-config\n",
		"conditional include": "[includeIf \"gitdir:**/repository/**\"]\n\tpath = ../outside-config\n",
		"case folded include": "[InClUdEiF \"onbranch:main\"]\n\tpath = ../outside-config\n",
		"include key":         "[core]\n\tinclude.path = ../outside-config\n",
	} {
		t.Run(name, func(t *testing.T) {
			repository := fakeRepository(t)
			outside := filepath.Join(filepath.Dir(repository), "outside-config")
			if err := os.WriteFile(outside, []byte("[remote \"origin\"]\n\tpromisor = true\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repository, ".git", "config"), []byte("[core]\n\trepositoryformatversion = 0\n"+section), 0o600); err != nil {
				t.Fatal(err)
			}
			started := filepath.Join(filepath.Dir(repository), "ordinary-git-started")
			git := writeFakeGit(t, fmt.Sprintf("#!/bin/sh\n: > %q\nexit 99\n", started))
			_, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), nil)
			if err == nil {
				t.Fatal("local config include was accepted")
			}
			if _, statErr := os.Lstat(started); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("ordinary Git ran before include refusal: %v", statErr)
			}
		})
	}
}

func TestLocalGitConfigValidatorIsMinimalAndFailClosed(t *testing.T) {
	valid := []byte("[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n[remote \"origin\"]\n\tpromisor = false\n")
	if !validLocalGitConfig(valid) {
		t.Fatal("ordinary local Git config rejected")
	}
	for name, config := range map[string][]byte{
		"BOM":                  append([]byte{0xef, 0xbb, 0xbf}, []byte("[core]\n\tbare = false\n")...),
		"non-ASCII":            []byte("[user]\n\tname = Jos\xc3\xa9\n"),
		"control":              []byte("[core]\n\tbare = \x01false\n"),
		"DEL control":          []byte("[core]\n\tbare = false\x7f\n"),
		"UTF-16":               []byte{'[', 0, 'c', 0, 'o', 0, 'r', 0, 'e', 0, ']', 0},
		"bare carriage return": []byte("[core]\rbare = false\n"),
		"include section":      []byte("[include]\n\tpath = ../outside\n"),
		"includeIf section":    []byte("[includeIf \"onbranch:main\"]\n\tpath = ../outside\n"),
		"include key":          []byte("[core]\n\tinclude.path = ../outside\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if validLocalGitConfig(config) {
				t.Fatal("unsupported local Git config accepted")
			}
		})
	}
}

func TestSelectGitRejectsBOMIncludeAndWorktreeConfigBeforeTrustedGit(t *testing.T) {
	t.Run("DEL control", func(t *testing.T) {
		fixture := newLocalGitFixture(t, "sha1")
		if err := os.WriteFile(filepath.Join(fixture.repository, ".git", "config"), []byte("[core]\n\tbare = false\x7f\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertTrustedConfigRejectedWithoutFIFOBlock(t, fixture, 750*time.Millisecond)
		started := 0
		_, err := selectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity, func(event gitProcessEvent) {
			if event == gitProcessStarted {
				started++
			}
		})
		if err == nil {
			t.Fatal("DEL control was accepted")
		}
		if started != 0 {
			t.Fatalf("Git started %d times before DEL control refusal", started)
		}
	})
	t.Run("BOM include FIFO", func(t *testing.T) {
		fixture := newLocalGitFixture(t, "sha1")
		fifo := filepath.Join(filepath.Dir(fixture.repository), "external-config.fifo")
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		config := append([]byte{0xef, 0xbb, 0xbf}, []byte("[include]\n\tpath = "+fifo+"\n")...)
		if err := os.WriteFile(filepath.Join(fixture.repository, ".git", "config"), config, 0o600); err != nil {
			t.Fatal(err)
		}
		assertTrustedConfigRejectedWithoutFIFOBlock(t, fixture, 750*time.Millisecond)
	})
	t.Run("worktree config include FIFO", func(t *testing.T) {
		fixture := newLocalGitFixture(t, "sha1")
		runFixtureGit(t, fixture.git, fixture.repository, "config", "core.repositoryformatversion", "1")
		runFixtureGit(t, fixture.git, fixture.repository, "config", "extensions.worktreeConfig", "true")
		fifo := filepath.Join(filepath.Dir(fixture.repository), "external-worktree-config.fifo")
		if err := unix.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.repository, ".git", "config.worktree"), []byte("[include]\n\tpath = "+fifo+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertTrustedConfigRejectedWithoutFIFOBlock(t, fixture, 750*time.Millisecond)
	})
}

func assertTrustedConfigRejectedWithoutFIFOBlock(t testing.TB, fixture localGitFixture, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := SelectGit(ctx, fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err == nil {
		t.Fatal("unsafe Git config was accepted")
	}
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("unsafe Git config crossed into the Git child: %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unsafe Git config blocked on its external FIFO")
	}
}

func TestGitAdminFilesAndObjectStoreAreExactAndRechecked(t *testing.T) {
	t.Run("config.worktree presence", func(t *testing.T) {
		for _, kind := range []string{"file", "directory", "symlink", "fifo"} {
			t.Run(kind, func(t *testing.T) {
				repository := fakeRepository(t)
				path := filepath.Join(repository, ".git", "config.worktree")
				switch kind {
				case "file":
					if err := os.WriteFile(path, []byte("[core]\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					if err := os.Symlink(filepath.Join(filepath.Dir(repository), "outside"), path); err != nil {
						t.Fatal(err)
					}
				case "fifo":
					if err := unix.Mkfifo(path, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				started := filepath.Join(filepath.Dir(repository), "git-started")
				git := writeFakeGit(t, fmt.Sprintf("#!/bin/sh\n: > %q\nexit 99\n", started))
				if _, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), nil); err == nil {
					t.Fatal("config.worktree entry was accepted")
				}
				if _, err := os.Lstat(started); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Git ran before config.worktree refusal: %v", err)
				}
			})
		}
	})
	t.Run("config symlink", func(t *testing.T) {
		repository := fakeRepository(t)
		config := filepath.Join(repository, ".git", "config")
		outside := filepath.Join(filepath.Dir(repository), "private-config")
		if err := os.WriteFile(outside, []byte("[core]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(config); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, config); err != nil {
			t.Fatal(err)
		}
		git := writeFakeGit(t, "#!/bin/sh\nexit 99\n")
		if _, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), nil); err == nil {
			t.Fatal("symlink Git config accepted")
		}
	})
	t.Run("objects symlink", func(t *testing.T) {
		repository := fakeRepository(t)
		objects := filepath.Join(repository, ".git", "objects")
		outside := filepath.Join(filepath.Dir(repository), "external-objects")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(objects); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, objects); err != nil {
			t.Fatal(err)
		}
		git := writeFakeGit(t, "#!/bin/sh\nexit 99\n")
		if _, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), nil); err == nil {
			t.Fatal("external symlink object store accepted")
		}
	})
	for _, adminName := range []string{"config", "objects"} {
		t.Run(adminName+" swapped between phases", func(t *testing.T) {
			repository := fakeRepository(t)
			logPath := filepath.Join(filepath.Dir(repository), "commands")
			git := writeFakeGit(t, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 1\n", logPath))
			mutated := false
			_, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), func(event gitProcessEvent) {
				if event != gitProcessWaited || mutated {
					return
				}
				mutated = true
				path := filepath.Join(repository, ".git", adminName)
				old := path + "-old"
				if renameErr := os.Rename(path, old); renameErr != nil {
					panic(renameErr)
				}
				if adminName == "config" {
					if writeErr := os.WriteFile(path, mustReadFile(t, old), 0o600); writeErr != nil {
						panic(writeErr)
					}
				} else if mkdirErr := os.Mkdir(path, 0o700); mkdirErr != nil {
					panic(mkdirErr)
				}
			})
			if err == nil {
				t.Fatal("Git administrative identity swap crossed phase boundary")
			}
			if lines := bytes.Count(mustReadFile(t, logPath), []byte{'\n'}); lines != 1 {
				t.Fatalf("replacement Git phase ran after admin swap: command lines=%d", lines)
			}
		})
	}
}

func TestGitRepositoryRejectsWritableAuthorityBeforeGit(t *testing.T) {
	type attack struct {
		name     string
		relative string
		mode     os.FileMode
	}
	attacks := []attack{
		{name: "group-writable root", mode: 0o720},
		{name: "world-writable root", mode: 0o702},
		{name: "group-writable Git directory", relative: ".git", mode: 0o720},
		{name: "world-writable Git directory", relative: ".git", mode: 0o702},
		{name: "group-writable config", relative: ".git/config", mode: 0o620},
		{name: "world-writable config", relative: ".git/config", mode: 0o602},
		{name: "group-writable objects", relative: ".git/objects", mode: 0o720},
		{name: "world-writable objects", relative: ".git/objects", mode: 0o702},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			repository := fakeRepository(t)
			path := repository
			if attack.relative != "" {
				path = filepath.Join(repository, attack.relative)
			}
			if err := os.Chmod(path, attack.mode); err != nil {
				t.Fatal(err)
			}
			assertRepositoryRejectedBeforeGit(t, repository, mustRepositoryIdentity(t, repository))
		})
	}
}

func TestGitSelectionToleratesUnrelatedObjectStoreChurn(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	unrelated := filepath.Join(fixture.repository, ".git", "objects", "aa", strings.Repeat("0", 38))
	if err := os.MkdirAll(filepath.Dir(unrelated), 0o700); err != nil {
		t.Fatal(err)
	}
	mutated := false
	selected, err := selectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity, func(event gitProcessEvent) {
		if event != gitProcessWaited || mutated {
			return
		}
		mutated = true
		if writeErr := os.WriteFile(unrelated, []byte("unrelated object churn"), 0o600); writeErr != nil {
			panic(writeErr)
		}
	})
	if err != nil {
		t.Fatalf("unrelated object-store churn rejected: %v", err)
	}
	if !mutated {
		t.Fatal("fixture did not mutate the object store between Git phases")
	}
	if !selected.Base().equal(fixture.base) {
		t.Fatal("unrelated object-store churn changed the selected base")
	}
}

func assertRepositoryRejectedBeforeGit(t testing.TB, repository string, identity RepositoryIdentity) {
	t.Helper()
	witness := filepath.Join(filepath.Dir(repository), "git-started")
	git := writeFakeGit(t, fmt.Sprintf("#!/bin/sh\n: > %q\nexit 99\n", witness))
	if _, err := selectGit(context.Background(), git, repository, "HEAD", identity, nil); err == nil {
		t.Fatal("unsafe Git object store was accepted")
	}
	if _, err := os.Lstat(witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Git ran before object-store refusal: %v", err)
	}
}

func TestGitAuthorityIsRecheckedAtProcessBoundaries(t *testing.T) {
	repository := fakeRepository(t)
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{0x31}, format.OIDLength()))
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *" config "*) exit 1 ;;
  *" rev-parse "*) printf '%%s\nsha1\n%%s\n' %q %q ;;
esac
`, repository, base.Hex())
	git := writeFakeGit(t, script)
	original := repository + "-original"
	mutated := false
	_, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), func(event gitProcessEvent) {
		if event != gitProcessWaited || mutated {
			return
		}
		mutated = true
		if renameErr := os.Rename(repository, original); renameErr != nil {
			panic(renameErr)
		}
		if mkdirErr := os.Mkdir(repository, 0o700); mkdirErr != nil {
			panic(mkdirErr)
		}
		if mkdirErr := os.Mkdir(filepath.Join(repository, ".git"), 0o700); mkdirErr != nil {
			panic(mkdirErr)
		}
	})
	if err == nil {
		t.Fatal("repository replacement after metadata child exit was accepted")
	}

}

func TestGitExecutableIsContentFrozenAcrossEveryPhase(t *testing.T) {
	for _, mutation := range []string{"same-size bytes", "changed-size", "mode", "timestamps"} {
		t.Run(mutation, func(t *testing.T) {
			repository := fakeRepository(t)
			logPath := filepath.Join(filepath.Dir(repository), "commands")
			startedAfterMutation := filepath.Join(filepath.Dir(repository), "replacement-ran")
			original := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 1\n# %s\n", logPath, strings.Repeat("padding", 20))
			git := writeFakeGit(t, original)
			mutated := false
			_, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), func(event gitProcessEvent) {
				if event != gitProcessWaited || mutated {
					return
				}
				mutated = true
				switch mutation {
				case "same-size bytes":
					replacement := fmt.Sprintf("#!/bin/sh\n: > %q\nexit 77\n", startedAfterMutation)
					if len(replacement) > len(original) {
						panic("test replacement exceeds original")
					}
					replacement += strings.Repeat("#", len(original)-len(replacement))
					if writeErr := os.WriteFile(git, []byte(replacement), 0o700); writeErr != nil {
						panic(writeErr)
					}
				case "changed-size":
					if writeErr := os.WriteFile(git, []byte("#!/bin/sh\nexit 77\n"), 0o700); writeErr != nil {
						panic(writeErr)
					}
				case "mode":
					if chmodErr := os.Chmod(git, 0o500); chmodErr != nil {
						panic(chmodErr)
					}
				case "timestamps":
					when := time.Unix(1, 0)
					if chtimeErr := os.Chtimes(git, when, when); chtimeErr != nil {
						panic(chtimeErr)
					}
				}
			})
			if err == nil {
				t.Fatal("in-place Git executable mutation was accepted")
			}
			if _, statErr := os.Lstat(startedAfterMutation); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("mutated executable ran another phase: %v", statErr)
			}
			if lines := bytes.Count(mustReadFile(t, logPath), []byte{'\n'}); lines != 1 {
				t.Fatalf("unexpected phases executed after mutation: %d", lines)
			}
		})
	}
}

func TestGitExecutableMustBeActualNativeGitForHost(t *testing.T) {
	actual := fixtureGitExecutable(t)
	if _, err := checkpointTrustedGitExecutable(actual); err != nil {
		t.Fatalf("Command Line Tools Git rejected: %v", err)
	}
	if TrustedDeveloperGitPath("/Applications/Xcode.app/Contents/Developer/usr/bin/git") {
		t.Fatal("Git below group-writable /Applications was trusted")
	}
	if _, err := checkpointTrustedGitExecutable("/usr/bin/git"); err == nil {
		t.Fatal("Apple xcode-select Git shim accepted as the committed executable")
	}
	shebang := filepath.Join(secureTempDir(t), "git")
	if err := os.WriteFile(shebang, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpointGitExecutable(shebang); err == nil {
		t.Fatal("shebang Git fake accepted")
	}
	nonGit := filepath.Join(secureTempDir(t), "not-git")
	copyTestExecutable(t, actual, nonGit)
	if _, err := checkpointGitExecutable(nonGit); err == nil {
		t.Fatal("native non-git locator accepted")
	}
	nativeFake := writeFakeGit(t, "#!/bin/sh\nexit 1\n")
	if _, err := checkpointGitExecutable(nativeFake); err != nil {
		t.Fatalf("native test Git fake rejected: %v", err)
	}
	witness := filepath.Join(secureTempDir(t), "renamed-native-ran")
	nonGitRenamed := writeFakeGit(t, fmt.Sprintf("#!/bin/sh\n: > %q\nexit 99\n", witness))
	repository := fakeRepository(t)
	if _, err := SelectGit(context.Background(), nonGitRenamed, repository, "HEAD", mustRepositoryIdentity(t, repository)); err == nil {
		t.Fatal("renamed native non-Git binary passed Developer-toolchain authority")
	}
	if _, err := os.Lstat(witness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renamed native non-Git binary executed: %v", err)
	}

	wrongArchitecture := filepath.Join(secureTempDir(t), "git")
	architecture := "x86_64"
	if runtime.GOARCH == "amd64" {
		architecture = "arm64"
	}
	command := exec.Command("/usr/bin/lipo", actual, "-thin", architecture, "-output", wrongArchitecture)
	command.Env = []string{"HOME=/dev/null", "TMPDIR=" + os.Getenv("TMPDIR"), "LC_ALL=C", "LANG=C"}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create wrong-architecture Git fixture: %v: %s", err, output)
	}
	if err := os.Chmod(wrongArchitecture, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := checkpointGitExecutable(wrongArchitecture); err == nil {
		t.Fatal("wrong-architecture Git accepted")
	}
}

func copyTestExecutable(t testing.TB, sourcePath, targetPath string) {
	t.Helper()
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGitPublicFailuresNeverExposePrivateBoundaryData(t *testing.T) {
	const sentinel = "DARK-FACTORY-PRIVATE-SENTINEL"
	assertPrivate := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("private failure fixture unexpectedly succeeded")
		}
		encoded, jsonErr := json.Marshal(err)
		if jsonErr != nil {
			t.Fatal(jsonErr)
		}
		texts := []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err), string(encoded)}
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
			texts = append(texts, cause.Error(), fmt.Sprintf("%#v", cause))
		}
		for _, text := range texts {
			if strings.Contains(text, sentinel) {
				t.Fatalf("public failure leaked private sentinel: %q", text)
			}
		}
	}

	repository := fakeRepository(t)
	git := writeFakeGit(t, "#!/bin/sh\nexit 1\n")
	_, err := SelectGit(context.Background(), git, filepath.Join(filepath.Dir(repository), sentinel), "HEAD", mustRepositoryIdentity(t, repository))
	assertPrivate(t, err)
	_, err = SelectGit(context.Background(), filepath.Join(filepath.Dir(repository), sentinel), repository, "HEAD", mustRepositoryIdentity(t, repository))
	assertPrivate(t, err)

	stderrGit := writeFakeGit(t, "#!/bin/sh\nprintf '"+sentinel+"\\n' >&2\nexit 91\n")
	_, err = selectGit(context.Background(), stderrGit, repository, "HEAD", mustRepositoryIdentity(t, repository), nil)
	assertPrivate(t, err)
	protocolGit := writeFakeGit(t, "#!/bin/sh\ncase \"$*\" in *\" config \"*) exit 1;; *) printf '"+sentinel+"\\n';; esac\n")
	_, err = selectGit(context.Background(), protocolGit, repository, "HEAD", mustRepositoryIdentity(t, repository), nil)
	assertPrivate(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = selectGit(ctx, git, repository, "HEAD", mustRepositoryIdentity(t, repository), nil)
	assertPrivate(t, err)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sanitized cancellation lost errors.Is: %v", err)
	}
	gitErrorType := reflect.TypeOf(GitError{})
	for index := 0; index < gitErrorType.NumField(); index++ {
		if gitErrorType.Field(index).IsExported() {
			t.Fatalf("GitError exports private field %s", gitErrorType.Field(index).Name)
		}
	}
	privateRepository := filepath.Join(filepath.Dir(repository), sentinel+"-repository")
	if err := os.Rename(repository, privateRepository); err != nil {
		t.Fatal(err)
	}
	selection := Selection{repositoryRoot: privateRepository, gitExecutable: filepath.Join(filepath.Dir(privateRepository), sentinel+"-git")}
	selectionText := fmt.Sprintf("%v|%+v|%#v", selection, selection, selection)
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(selectionText, sentinel) || strings.Contains(string(selectionJSON), sentinel) {
		t.Fatalf("public Selection formatting leaked private locators: %q %q", selectionText, selectionJSON)
	}
}

func TestReadGitCapturePreservesBoundedReaderContract(t *testing.T) {
	wrappedEOF := fmt.Errorf("wrapped: %w", io.EOF)
	tests := []struct {
		name     string
		reader   io.Reader
		maximum  int
		data     string
		overflow bool
		wantErr  error
	}{
		{name: "under limit", reader: strings.NewReader("abc"), maximum: 4, data: "abc"},
		{name: "exact limit", reader: strings.NewReader("abcd"), maximum: 4, data: "abcd"},
		{name: "over limit", reader: strings.NewReader("abcde"), maximum: 4, overflow: true},
		{name: "reader error after data", reader: io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(io.ErrUnexpectedEOF)), maximum: 4, wantErr: io.ErrUnexpectedEOF},
		{name: "wrapped EOF remains success", reader: io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(wrappedEOF)), maximum: 4, data: "abc"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := readGitCapture(test.reader, test.maximum)
			if result.overflow != test.overflow || string(result.data) != test.data || !errors.Is(result.err, test.wantErr) {
				t.Fatalf("result=%+v, want data=%q overflow=%t err=%v", result, test.data, test.overflow, test.wantErr)
			}
		})
	}
}

func newLocalGitFixture(t testing.TB, formatName string) localGitFixture {
	t.Helper()
	git := fixtureGitExecutable(t)
	root := secureTempDir(t)
	repository := filepath.Join(root, "repository")
	command := exec.Command(git, "init", "--object-format="+formatName, repository)
	command.Env = fixtureGitEnvironment(root)
	if output, err := command.CombinedOutput(); err != nil {
		message := string(output)
		if formatName == "sha256" && (strings.Contains(message, "unknown value") || strings.Contains(message, "unsupported")) {
			t.Skipf("installed Git explicitly lacks SHA-256 repositories: %s", strings.TrimSpace(message))
		}
		t.Fatalf("git init %s: %v: %s", formatName, err, output)
	}
	runFixtureGit(t, git, repository, "config", "user.name", "Dark Factory Test")
	runFixtureGit(t, git, repository, "config", "user.email", "test@invalid")
	files := []fixtureFile{
		{[]byte("README.md"), "100644", []byte("old commit\n")},
		{[]byte("empty"), "100644", nil},
		{[]byte("nested/run"), "100755", []byte("#!/bin/sh\nexit 0\n")},
	}
	for _, file := range files {
		path := filepath.Join(repository, string(file.path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if file.mode == "100755" {
			mode = 0o755
		}
		if err := os.WriteFile(path, file.data, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	runFixtureGit(t, git, repository, "add", "--all")
	runFixtureGit(t, git, repository, "commit", "-m", "fixture")
	format := mustFormat(t, formatName)
	base, err := parseGitOID(format, []byte(strings.TrimSpace(runFixtureGitOutput(t, git, repository, "rev-parse", "HEAD"))))
	if err != nil {
		t.Fatal(err)
	}
	return localGitFixture{
		git: git, repository: repository, identity: mustRepositoryIdentity(t, repository),
		format: format, base: base, files: files,
	}
}

func mustFormat(t testing.TB, name string) ObjectFormat {
	t.Helper()
	format, err := NewObjectFormat(name)
	if err != nil {
		t.Fatal(err)
	}
	return format
}

func mustID(t testing.TB, format ObjectFormat, raw []byte) ObjectID {
	t.Helper()
	id, err := NewObjectID(format, raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func secureTempDir(t testing.TB) string {
	t.Helper()
	path, err := os.MkdirTemp("/private/tmp", "dark-factory-change-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func descriptorCount(t testing.TB) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// assertExactTree checks the fixture's files, and nothing else besides the
// gitfile, are in the worktree with their exact bytes and modes.
func assertExactTree(t testing.TB, root string, fixture localGitFixture) {
	t.Helper()
	want := make(map[string]fixtureFile, len(fixture.files))
	for _, file := range fixture.files {
		want[string(file.path)] = file
	}
	seen := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		if entry.IsDir() || relative == ".git" {
			return nil
		}
		file, ok := want[relative]
		if !ok {
			return fmt.Errorf("unexpected entry %q", relative)
		}
		seen++
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		executable := info.Mode()&0o100 != 0
		if !bytes.Equal(data, file.data) || executable != (file.mode == "100755") {
			return fmt.Errorf("entry %q differs: %q executable=%v", relative, data, executable)
		}
		return nil
	})
	if err != nil || seen != len(want) {
		t.Fatalf("worktree differs from fixture: %v (seen %d of %d)", err, seen, len(want))
	}
}

func fixtureGitExecutable(t testing.TB) string {
	t.Helper()
	if _, err := os.Stat(TrustedGitExecutable); err != nil {
		t.Fatalf("Command Line Tools Git is unavailable: %v", err)
	}
	return TrustedGitExecutable
}

func mustRepositoryIdentity(t testing.TB, path string) RepositoryIdentity {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := repositoryIdentityOf(info)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func runFixtureGit(t testing.TB, git, repository string, arguments ...string) {
	t.Helper()
	_ = runFixtureGitOutput(t, git, repository, arguments...)
}

func runFixtureGitOutput(t testing.TB, git, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command(git, append([]string{"-C", repository}, arguments...)...)
	command.Env = fixtureGitEnvironment(filepath.Dir(repository))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func fixtureGitEnvironment(home string) []string {
	return []string{
		"HOME=" + home, "TMPDIR=" + home, "LC_ALL=C", "LANG=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1",
	}
}

func TestGitBoundaryResourceCensus(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	homesBefore, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "dark-factory-git-home-*"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := newLocalGitFixture(t, "sha1")
	previousGC := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(previousGC) })
	beforeFDs := descriptorCount(t)
	beforeGoroutines := runtime.NumGoroutine()
	var selected Selection
	for range 40 {
		var err error
		selected, err = SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
		if err != nil {
			t.Fatal(err)
		}
	}
	if after := descriptorCount(t); after != beforeFDs {
		t.Fatalf("40 public SelectGit calls leaked descriptors without GC: before=%d after=%d", beforeFDs, after)
	}
	parent := secureTempDir(t)
	for index := range 5 {
		changeID := strings.Repeat(fmt.Sprintf("%x", index+1), 32)
		if _, err := AddWorktree(context.Background(), selected, filepath.Join(parent, changeID), BranchName(changeID)); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectWorktree(context.Background(), fixture.git, fixture.repository, fixture.identity, filepath.Join(parent, changeID)); err != nil {
			t.Fatal(err)
		}
	}
	blockedGit := writeFakeGit(t, "#!/bin/sh\nexec /usr/bin/perl -e '$SIG{TERM}=sub{exit 0}; while(1){select(undef,undef,undef,1)}'\n")
	blockedRepository := fakeRepository(t)
	for range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := selectGit(ctx, blockedGit, blockedRepository, "HEAD", mustRepositoryIdentity(t, blockedRepository), nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked resource-census selection=%v", err)
		}
	}
	if after := descriptorCount(t); after != beforeFDs {
		t.Fatalf("Git worktree and cancel paths leaked descriptors without GC: before=%d after=%d", beforeFDs, after)
	}
	if after := runtime.NumGoroutine(); after > beforeGoroutines {
		t.Fatalf("Git boundary goroutine leak: before=%d after=%d", beforeGoroutines, after)
	}
	homesAfter, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "dark-factory-git-home-*"))
	if err != nil || !reflect.DeepEqual(homesAfter, homesBefore) {
		t.Fatalf("Git HOME census before=%v after=%v err=%v", homesBefore, homesAfter, err)
	}
}

func TestFreshSelectionFetchesConfiguredUpstreamWithoutMovingCheckout(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	remote := newLocalGitFixture(t, "sha1")
	runFixtureGit(t, fixture.git, fixture.repository, "remote", "add", "upstream", remote.repository)
	// Fixture-only local transport; production keeps Git's protocol policy.
	runFixtureGit(t, fixture.git, fixture.repository, "config", "protocol.file.allow", "always")
	branch := strings.TrimSpace(runFixtureGitOutput(t, remote.git, remote.repository, "symbolic-ref", "HEAD"))
	localBranch := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "branch", "--show-current"))
	runFixtureGit(t, fixture.git, fixture.repository, "config", "branch."+localBranch+".remote", "upstream")
	runFixtureGit(t, fixture.git, fixture.repository, "config", "branch."+localBranch+".merge", branch)
	if err := os.WriteFile(filepath.Join(remote.repository, "new-source.txt"), []byte("new source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, remote.git, remote.repository, "add", "new-source.txt")
	runFixtureGit(t, remote.git, remote.repository, "commit", "-m", "remote advances")
	want := strings.TrimSpace(runFixtureGitOutput(t, remote.git, remote.repository, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(fixture.repository, "README.md"), []byte("uncommitted operator work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkout := runFixtureGitOutput(t, fixture.git, fixture.repository, "status", "--porcelain")
	tracking := runFixtureGitOutput(t, fixture.git, fixture.repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/")
	start, results := make(chan struct{}), make(chan error, 8)
	for range cap(results) {
		go func() {
			<-start
			selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
			if err == nil && selected.Base().Hex() != want {
				err = fmt.Errorf("concurrent source=%s want=%s", selected.Base().Hex(), want)
			}
			results <- err
		}()
	}
	close(start)
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatalf("parallel fresh selection: %v", err)
		}
	}
	for _, policy := range []string{"HEAD", "refs/remotes/upstream/" + strings.TrimPrefix(branch, "refs/heads/")} {
		selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, policy, fixture.identity)
		if err != nil || selected.Base().Hex() != want {
			t.Fatalf("policy=%s source=%s want=%s err=%v", policy, selected.Base().Hex(), want, err)
		}
	}
	if got := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "HEAD")); got != fixture.base.Hex() {
		t.Fatalf("checkout moved to %s", got)
	}
	if got := runFixtureGitOutput(t, fixture.git, fixture.repository, "status", "--porcelain"); got != checkout {
		t.Fatalf("checkout changed: %q -> %q", checkout, got)
	}
	if got := runFixtureGitOutput(t, fixture.git, fixture.repository, "for-each-ref", "--format=%(refname) %(objectname)", "refs/remotes/"); got != tracking {
		t.Fatalf("tracking refs changed: %q -> %q", tracking, got)
	}
	if _, err := os.Stat(filepath.Join(fixture.repository, ".git", "FETCH_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("fetch wrote shared FETCH_HEAD: %v", err)
	}
	// Custom remote mappings are read policy, never permission to rewrite a
	// local branch. Fetching only the pinned objects preserves this branch.
	localTarget := "refs/heads/operator-work"
	runFixtureGit(t, fixture.git, fixture.repository, "update-ref", localTarget, fixture.base.Hex())
	runFixtureGit(t, fixture.git, fixture.repository, "config", "--replace-all", "remote.upstream.fetch", "+"+branch+":"+localTarget)
	custom, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil || custom.Base().Hex() != want {
		t.Fatalf("custom upstream source: %v", err)
	}
	if got := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", localTarget)); got != fixture.base.Hex() {
		t.Fatalf("operator branch changed: %s", got)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "config", "--replace-all", "remote.upstream.fetch", "+refs/heads/*:refs/remotes/upstream/*")
	// A deleted branch cannot fall back to previously fetched objects.
	runFixtureGit(t, remote.git, remote.repository, "update-ref", "-d", branch)
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil {
		t.Fatal("deleted remote branch silently selected old source")
	}
	runFixtureGit(t, remote.git, remote.repository, "update-ref", branch, want)
	// A cached successful tracking ref must not hide a subsequent failed fetch.
	runFixtureGit(t, fixture.git, fixture.repository, "remote", "set-url", "upstream", filepath.Join(t.TempDir(), "missing"))
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil || !strings.Contains(err.Error(), "source refresh failed") {
		t.Fatalf("failed fetch fell back to old source: %v", err)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "config", "branch."+localBranch+".remote", "unconfigured-remote")
	if _, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity); err == nil {
		t.Fatal("broken tracking configuration silently selected local source")
	}
	// Explicit local pins and detached HEAD remain usable without the remote.
	for _, policy := range []string{fixture.base.Hex(), "refs/heads/" + localBranch} {
		selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, policy, fixture.identity)
		if err != nil || selected.Base().Hex() != fixture.base.Hex() {
			t.Fatalf("local policy=%s err=%v", policy, err)
		}
	}
	runFixtureGit(t, fixture.git, fixture.repository, "checkout", "--detach", fixture.base.Hex())
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil || selected.Base().Hex() != fixture.base.Hex() {
		t.Fatalf("detached source: %v", err)
	}
}
