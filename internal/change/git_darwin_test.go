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
	"time"

	"golang.org/x/sys/unix"
)

type localGitFixture struct {
	git        string
	repository string
	identity   RepositoryIdentity
	format     ObjectFormat
	base       ObjectID
	manifest   Manifest
	blobs      map[string][]byte
}

func TestSelectGitRealSHA1AndSHA256WithoutBlobReads(t *testing.T) {
	for _, formatName := range []string{"sha1", "sha256"} {
		t.Run(formatName, func(t *testing.T) {
			fixture := newLocalGitFixture(t, formatName)
			selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
			if err != nil {
				t.Fatal(err)
			}
			if !selected.RepositoryIdentity().Equal(fixture.identity) || selected.ObjectFormat() != fixture.format ||
				!selected.Base().equal(fixture.base) || !selected.Commitment().Equal(fixture.manifest.Commitment()) ||
				selected.EntryCount() != fixture.manifest.EntryCount() || selected.BlobBytes() != fixture.manifest.BlobBytes() ||
				!manifestsEqual(selected.Manifest(), fixture.manifest) {
				t.Fatalf("selection differs: %+v", selected)
			}
		})
	}
}

func TestSelectionSurvivesRefAndWorktreeMovementAndMaterializesExactOldCommit(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	if err := os.WriteFile(filepath.Join(fixture.repository, "README.md"), []byte("new commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "add", "README.md")
	runFixtureGit(t, fixture.git, fixture.repository, "commit", "-m", "move HEAD")
	newCommit := strings.TrimSpace(runFixtureGitOutput(t, fixture.git, fixture.repository, "rev-parse", "HEAD"))
	runFixtureGit(t, fixture.git, fixture.repository, "update-ref", "refs/heads/moving", fixture.base.Hex())
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "moving", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, fixture.git, fixture.repository, "update-ref", "refs/heads/moving", newCommit)
	if err := os.WriteFile(filepath.Join(fixture.repository, "README.md"), []byte("uncommitted worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changeParent := secureTempDir(t)
	prepared := mustPrepare(t, changeParent, "published", "declared-stage")
	blobs, err := OpenGitBlobs(context.Background(), fixture.git, fixture.repository, selected)
	if err != nil {
		t.Fatal(err)
	}
	published, err := prepared.PopulateAndPublish(context.Background(), selected.Manifest(), blobs.Read)
	if err != nil {
		_ = blobs.Abort()
		t.Fatal(err)
	}
	if err := blobs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if !published.Facts().Commitment().Equal(selected.Commitment()) {
		t.Fatalf("published commitment=%s selected=%s", published.Facts().Commitment().Hex(), selected.Commitment().Hex())
	}
	assertExactTree(t, published.Path(), changeFixture{manifest: fixture.manifest, blobs: fixture.blobs})
	readme, err := os.ReadFile(filepath.Join(published.Path(), "README.md"))
	if err != nil || string(readme) != "old commit\n" {
		t.Fatalf("published moving state: %q %v", readme, err)
	}
	if _, err := os.Lstat(filepath.Join(published.Path(), ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published Change contains Git authority: %v", err)
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
	if _, err := OpenGitBlobs(context.Background(), fixture.git, fixture.repository, selected); err == nil {
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
		prepare  func(testing.TB, string)
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
		{
			name: "group-writable nested object directory", relative: ".git/objects/aa", mode: 0o720,
			prepare: func(t testing.TB, path string) {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "world-writable nested object file", relative: ".git/objects/aa/" + strings.Repeat("0", 38), mode: 0o602,
			prepare: func(t testing.TB, path string) {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("object"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "group-writable object admin file", relative: ".git/objects/info/packs", mode: 0o620,
			prepare: func(t testing.TB, path string) {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			repository := fakeRepository(t)
			path := repository
			if attack.relative != "" {
				path = filepath.Join(repository, attack.relative)
			}
			if attack.prepare != nil {
				attack.prepare(t, path)
			}
			if err := os.Chmod(path, attack.mode); err != nil {
				t.Fatal(err)
			}
			assertObjectStoreRejectedBeforeGit(t, repository, mustRepositoryIdentity(t, repository))
		})
	}
}

func TestGitObjectStoreRejectsNestedIndirectionBeforeGit(t *testing.T) {
	t.Run("all loose fanouts symlinked outside", func(t *testing.T) {
		fixture := newLocalGitFixture(t, "sha1")
		objects := filepath.Join(fixture.repository, ".git", "objects")
		outside := filepath.Join(filepath.Dir(fixture.repository), "external-loose")
		if err := os.Mkdir(outside, 0o700); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(objects)
		if err != nil {
			t.Fatal(err)
		}
		moved := 0
		for _, entry := range entries {
			if !entry.IsDir() || len(entry.Name()) != 2 || !isLowerHex(entry.Name()) {
				continue
			}
			from := filepath.Join(objects, entry.Name())
			to := filepath.Join(outside, entry.Name())
			if err := os.Rename(from, to); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(to, from); err != nil {
				t.Fatal(err)
			}
			moved++
		}
		if moved == 0 {
			t.Fatal("fixture produced no loose fanouts")
		}
		assertObjectStoreRejectedBeforeGit(t, fixture.repository, fixture.identity)
	})

	for _, linkKind := range []string{"symlink", "hardlink"} {
		t.Run("loose object "+linkKind, func(t *testing.T) {
			fixture := newLocalGitFixture(t, "sha1")
			object := firstLooseObject(t, fixture.repository)
			outside := filepath.Join(filepath.Dir(fixture.repository), "external-object")
			if linkKind == "symlink" {
				if err := os.Rename(object, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, object); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Link(object, outside); err != nil {
				t.Fatal(err)
			}
			assertObjectStoreRejectedBeforeGit(t, fixture.repository, fixture.identity)
		})
	}

	t.Run("pack directory symlink", func(t *testing.T) {
		fixture := newLocalGitFixture(t, "sha1")
		pack := filepath.Join(fixture.repository, ".git", "objects", "pack")
		outside := filepath.Join(filepath.Dir(fixture.repository), "external-pack")
		if err := os.Rename(pack, outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, pack); err != nil {
			t.Fatal(err)
		}
		assertObjectStoreRejectedBeforeGit(t, fixture.repository, fixture.identity)
	})

	for _, attack := range []struct {
		name     string
		relative string
		hardlink bool
	}{
		{"pack symlink", "pack/pack-" + strings.Repeat("1", 40) + ".pack", false},
		{"index hardlink", "pack/pack-" + strings.Repeat("1", 40) + ".idx", true},
		{"multi-pack symlink", "pack/multi-pack-index", false},
		{"multi-pack bitmap hardlink", "pack/multi-pack-index-" + strings.Repeat("2", 40) + ".bitmap", true},
		{"commit graph symlink", "info/commit-graph", false},
		{"split commit graph hardlink", "info/commit-graphs/graph-" + strings.Repeat("3", 40) + ".graph", true},
	} {
		t.Run(attack.name, func(t *testing.T) {
			repository := fakeRepository(t)
			target := filepath.Join(repository, ".git", "objects", attack.relative)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(filepath.Dir(repository), strings.ReplaceAll(attack.name, " ", "-"))
			if err := os.WriteFile(outside, []byte("object artifact"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			if attack.hardlink {
				err = os.Link(outside, target)
			} else {
				err = os.Symlink(outside, target)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertObjectStoreRejectedBeforeGit(t, repository, mustRepositoryIdentity(t, repository))
		})
	}
}

func TestGitObjectStoreSupportsLooseAndPackedRepositoriesAndRecordsCost(t *testing.T) {
	for _, packed := range []bool{false, true} {
		name := "loose"
		if packed {
			name = "packed"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newLocalGitFixture(t, "sha1")
			if packed {
				runFixtureGit(t, fixture.git, fixture.repository, "gc", "--prune=now")
			}
			started := time.Now()
			selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
			if err != nil {
				t.Fatal(err)
			}
			if !selected.Commitment().Equal(fixture.manifest.Commitment()) {
				t.Fatal("object-store shape changed the selected commitment")
			}
			t.Logf("%s object-store checkpoint: entries=%d selection=%s", name, selected.repository.objectStore.entryCount, time.Since(started))
		})
	}
}

func TestGitObjectStoreBoundsGrammarAndPhaseIdentity(t *testing.T) {
	t.Run("bounded enumeration", func(t *testing.T) {
		repository := fakeRepository(t)
		fanout := filepath.Join(repository, ".git", "objects", "aa")
		if err := os.Mkdir(fanout, 0o700); err != nil {
			t.Fatal(err)
		}
		for index := range 4 {
			name := fmt.Sprintf("%038x", index+1)
			if err := os.WriteFile(filepath.Join(fanout, name), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		fd, err := unix.Open(filepath.Join(repository, ".git", "objects"), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		if _, err := checkpointGitObjectStore(fd, 3); err == nil {
			t.Fatal("bounded scanner accepted more than its exact entry budget")
		}
	})
	for _, invalid := range []struct {
		name string
		make func(testing.TB, string)
	}{
		{"unknown", func(t testing.TB, objects string) { mustWriteTestFile(t, filepath.Join(objects, "mystery")) }},
		{"depth", func(t testing.TB, objects string) {
			if err := os.MkdirAll(filepath.Join(objects, "info", "commit-graphs", "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"fifo", func(t testing.TB, objects string) {
			fanout := filepath.Join(objects, "aa")
			if err := os.Mkdir(fanout, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(filepath.Join(fanout, strings.Repeat("0", 38)), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			repository := fakeRepository(t)
			invalid.make(t, filepath.Join(repository, ".git", "objects"))
			assertObjectStoreRejectedBeforeGit(t, repository, mustRepositoryIdentity(t, repository))
		})
	}

	for _, relative := range []string{"aa/" + strings.Repeat("0", 38), "pack/pack-" + strings.Repeat("1", 40) + ".pack"} {
		t.Run("phase swap "+relative[:2], func(t *testing.T) {
			repository := fakeRepository(t)
			object := filepath.Join(repository, ".git", "objects", relative)
			if err := os.MkdirAll(filepath.Dir(object), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(object, []byte("stable bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(filepath.Dir(repository), "commands")
			git := writeFakeGit(t, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 1\n", logPath))
			mutated := false
			_, err := selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), func(event gitProcessEvent) {
				if event != gitProcessWaited || mutated {
					return
				}
				mutated = true
				old := object + ".old"
				if renameErr := os.Rename(object, old); renameErr != nil {
					panic(renameErr)
				}
				if writeErr := os.WriteFile(object, mustReadFile(t, old), 0o600); writeErr != nil {
					panic(writeErr)
				}
			})
			if err == nil {
				t.Fatal("object-store identity swap crossed a Git phase")
			}
			if lines := bytes.Count(mustReadFile(t, logPath), []byte{'\n'}); lines != 1 {
				t.Fatalf("Git continued after object-store swap: command lines=%d", lines)
			}
		})
	}
}

func assertObjectStoreRejectedBeforeGit(t testing.TB, repository string, identity RepositoryIdentity) {
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

func firstLooseObject(t testing.TB, repository string) string {
	t.Helper()
	objects := filepath.Join(repository, ".git", "objects")
	entries, err := os.ReadDir(objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) != 2 || !isLowerHex(entry.Name()) {
			continue
		}
		files, err := os.ReadDir(filepath.Join(objects, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) > 0 {
			return filepath.Join(objects, entry.Name(), files[0].Name())
		}
	}
	t.Fatal("fixture produced no loose object")
	return ""
}

func mustWriteTestFile(t testing.TB, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGitAuthorityIsRecheckedAtProcessBoundaries(t *testing.T) {
	repository := fakeRepository(t)
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{0x31}, format.OIDLength()))
	entry := mustEntry(t, format, []byte("file"), "100644", []byte("secret"))
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  *" config "*) exit 1 ;;
  *" rev-parse "*) printf '%%s\nsha1\n%%s\n' %q %q ;;
  *" ls-tree "*) printf '100644 blob %%s 6\tfile\0' %q ;;
esac
`, repository, base.Hex(), entry.oid.Hex())
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

	repository = fakeRepository(t)
	selection, _ := fakeSelection(t, repository, "", []byte("secret"))
	git = writeFakeGit(t, "#!/bin/sh\nwhile IFS= read -r request; do exit 0; done\n")
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	original = git + "-original"
	_, err = openGitBlobs(context.Background(), git, repository, selection, func(event gitProcessEvent) {
		if event != gitProcessStarted {
			return
		}
		if renameErr := os.Rename(git, original); renameErr != nil {
			panic(renameErr)
		}
		if writeErr := os.WriteFile(git, []byte("#!/bin/sh\nexit 0\n"), 0o700); writeErr != nil {
			panic(writeErr)
		}
	})
	if err == nil {
		t.Fatal("Git executable replacement after blob child start was accepted")
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
	selection, _ := fakeSelection(t, privateRepository, "", []byte("secret"))
	selection.gitExecutable = filepath.Join(filepath.Dir(privateRepository), sentinel+"-git")
	selectionText := fmt.Sprintf("%v|%+v|%#v", selection, selection, selection)
	selectionJSON, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(selectionText, sentinel) || strings.Contains(string(selectionJSON), sentinel) {
		t.Fatalf("public Selection formatting leaked private locators: %q %q", selectionText, selectionJSON)
	}
}

func TestParseGitTreeRejectsUnsupportedMalformedAndUnboundedMetadata(t *testing.T) {
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{1}, format.OIDLength()))
	oid := strings.Repeat("a", format.OIDLength()*2)
	invalid := map[string][]byte{
		"symlink":      []byte(fmt.Sprintf("120000 blob %s 1\tlink\x00", oid)),
		"gitlink":      []byte(fmt.Sprintf("160000 commit %s -\tsubmodule\x00", oid)),
		"tree":         []byte(fmt.Sprintf("040000 tree %s -\tdirectory\x00", oid)),
		"git path":     []byte(fmt.Sprintf("100644 blob %s 1\t.GIT/config\x00", oid)),
		"invalid utf8": append([]byte(fmt.Sprintf("100644 blob %s 1\tbad", oid)), 0xff, 0),
		"malformed":    []byte("not-a-record\x00"),
		"truncated":    []byte(fmt.Sprintf("100644 blob %s 1\tpath", oid)),
		"wrong oid":    []byte("100644 blob aa 1\tpath\x00"),
		"size":         []byte(fmt.Sprintf("100644 blob %s %d\tpath\x00", oid, MaxBlobBytes+1)),
	}
	for name, output := range invalid {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGitTree(format, base, output); err == nil {
				t.Fatal("unsafe tree metadata accepted")
			}
		})
	}
	duplicate := []byte(fmt.Sprintf("100644 blob %s 1\tpath\x00100644 blob %s 1\tpath\x00", oid, oid))
	if _, err := parseGitTree(format, base, duplicate); err == nil {
		t.Fatal("duplicate path accepted")
	}
	deep := strings.Repeat("d/", MaxDepth) + "f"
	if _, err := parseGitTree(format, base, []byte(fmt.Sprintf("100644 blob %s 1\t%s\x00", oid, deep))); err == nil {
		t.Fatal("over-depth path accepted")
	}
	total := make([]byte, 0)
	for index := range 5 {
		total = append(total, []byte(fmt.Sprintf("100644 blob %s %d\tf%d\x00", oid, MaxBlobBytes, index))...)
	}
	if _, err := parseGitTree(format, base, total); err == nil {
		t.Fatal("over-total-size tree accepted")
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
	entries := make([]Entry, len(files))
	blobs := make(map[string][]byte, len(files))
	for index, file := range files {
		entries[index] = mustEntry(t, format, file.path, file.mode, file.data)
		blobs[entries[index].oid.Hex()] = bytes.Clone(file.data)
	}
	return localGitFixture{
		git: git, repository: repository, identity: mustRepositoryIdentity(t, repository),
		format: format, base: base, manifest: mustManifest(t, format, base, entries), blobs: blobs,
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
	for range 20 {
		blobs, err := OpenGitBlobs(context.Background(), fixture.git, fixture.repository, selected)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range selected.Manifest().Entries() {
			if _, err := blobs.Read(context.Background(), entry.ObjectID()); err != nil {
				t.Fatal(err)
			}
		}
		if err := blobs.Close(); err != nil {
			t.Fatal(err)
		}
	}
	malformedRepository := fakeRepository(t)
	malformedSelection, malformedEntry := fakeSelection(t, malformedRepository, "", []byte("secret"))
	malformedGit := writeFakeGit(t, "#!/bin/sh\nIFS= read -r request || exit 2\nprintf '%s blob 6\\nwrong!\\n' \"$request\"\n")
	malformedSelection.gitExecutable, malformedSelection.gitIdentity = malformedGit, mustGitFileIdentity(t, malformedGit)
	for range 10 {
		blobs, err := openGitBlobs(context.Background(), malformedGit, malformedRepository, malformedSelection, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := blobs.Read(context.Background(), malformedEntry.oid); err == nil {
			t.Fatal("malformed resource-census blob was accepted")
		}
	}
	blockedRepository := fakeRepository(t)
	blockedSelection, blockedEntry := fakeSelection(t, blockedRepository, "", []byte("secret"))
	blockedGit := writeFakeGit(t, "#!/bin/sh\nIFS= read -r request || exit 2\nexec /usr/bin/perl -e '$SIG{TERM}=sub{exit 0}; while(1){select(undef,undef,undef,1)}'\n")
	blockedSelection.gitExecutable, blockedSelection.gitIdentity = blockedGit, mustGitFileIdentity(t, blockedGit)
	for range 5 {
		blobs, err := openGitBlobs(context.Background(), blockedGit, blockedRepository, blockedSelection, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err = blobs.Read(ctx, blockedEntry.oid)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked resource-census read=%v", err)
		}
	}
	if after := descriptorCount(t); after != beforeFDs {
		t.Fatalf("Git blob success/error/cancel paths leaked descriptors without GC: before=%d after=%d", beforeFDs, after)
	}
	if after := runtime.NumGoroutine(); after > beforeGoroutines {
		t.Fatalf("Git boundary goroutine leak: before=%d after=%d", beforeGoroutines, after)
	}
	homesAfter, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "dark-factory-git-home-*"))
	if err != nil || !reflect.DeepEqual(homesAfter, homesBefore) {
		t.Fatalf("Git HOME census before=%v after=%v err=%v", homesBefore, homesAfter, err)
	}
}
