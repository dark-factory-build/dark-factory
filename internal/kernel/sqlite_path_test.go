package kernel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRequiresCanonicalOwnerOnlyDatabaseParent(t *testing.T) {
	image, err := NewDatabaseImage(context.Background(), FactoryConfig{}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("noncanonical path", func(t *testing.T) {
		path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
		writeSidecar(t, path, image, 0o600)
		noncanonical := filepath.Join(filepath.Dir(path), ".", filepath.Base(path))
		if noncanonical == path {
			noncanonical = filepath.Dir(path) + string(filepath.Separator) + "." + string(filepath.Separator) + filepath.Base(path)
		}
		if _, err := Open(context.Background(), noncanonical); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("noncanonical path error = %v", err)
		}
	})

	t.Run("symlink ancestor", func(t *testing.T) {
		root := filepath.Dir(mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "placeholder")))
		realParent := filepath.Join(root, "real", "home")
		if err := os.MkdirAll(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(realParent, "kernel.db")
		writeSidecar(t, path, image, 0o600)
		if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linked")); err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(root, "linked", "home", "kernel.db")
		if _, err := Open(context.Background(), linked); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("symlink ancestor error = %v", err)
		}
	})

	t.Run("wrong final parent mode", func(t *testing.T) {
		path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
		writeSidecar(t, path, image, 0o600)
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(context.Background(), path); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("wrong parent mode error = %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("parent-mode refusal changed database")
		}
	})

	t.Run("final parent is not directory", func(t *testing.T) {
		root := filepath.Dir(mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "placeholder")))
		component := filepath.Join(root, "not-a-directory")
		writeSidecar(t, component, []byte("sentinel"), 0o600)
		if _, err := Open(context.Background(), filepath.Join(component, "kernel.db")); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("non-directory parent error = %v", err)
		}
	})
}

func TestDatabasePathAuthorityDetectsEveryRetainedComponentReplacement(t *testing.T) {
	for _, cut := range []string{"first", "second"} {
		t.Run(cut, func(t *testing.T) {
			root := filepath.Dir(mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "placeholder")))
			first := filepath.Join(root, "first")
			second := filepath.Join(first, "second")
			if err := os.MkdirAll(second, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(second, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(second, "kernel.db")
			authority, err := openDatabasePathAuthority(path)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()

			target := second
			if cut == "first" {
				target = first
			}
			if err := os.Rename(target, target+".old"); err != nil {
				t.Fatal(err)
			}
			if cut == "first" {
				if err := os.MkdirAll(second, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(second, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(second, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := authority.recheck(); !errors.Is(err, ErrCorruptState) {
				t.Fatalf("%s component replacement error = %v", cut, err)
			}
		})
	}

	t.Run("mode changed after pin", func(t *testing.T) {
		path := mustCanonicalTestDatabasePath(t, filepath.Join(t.TempDir(), "kernel.db"))
		authority, err := openDatabasePathAuthority(path)
		if err != nil {
			t.Fatal(err)
		}
		defer authority.Close()
		if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := authority.recheck(); !errors.Is(err, ErrForeignDatabase) {
			t.Fatalf("changed parent mode error = %v", err)
		}
	})
}
