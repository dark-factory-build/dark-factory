//go:build darwin || linux

package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTokenOpenRaceIsBoundedAndFailClosed(t *testing.T) {
	bearer := testCredential('Q')
	tests := []struct {
		name    string
		replace func(testing.TB, string, string) func()
	}{
		{name: "fifo", replace: func(t testing.TB, token, saved string) func() {
			if err := os.Rename(token, saved); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(token, 0o600); err != nil {
				t.Fatal(err)
			}
			cancel := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				select {
				case <-cancel:
					return
				case <-time.After(300 * time.Millisecond):
				}
				fd, err := syscall.Open(token, syscall.O_WRONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
				if err == nil {
					_ = syscall.Close(fd)
				}
			}()
			return func() {
				close(cancel)
				<-done
			}
		}},
		{name: "symlink", replace: func(t testing.TB, token, saved string) func() {
			if err := os.Rename(token, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Base(saved), token); err != nil {
				t.Fatal(err)
			}
			return func() {}
		}},
		{name: "directory", replace: func(t testing.TB, token, saved string) func() {
			if err := os.Rename(token, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(token, 0o700); err != nil {
				t.Fatal(err)
			}
			return func() {}
		}},
		{name: "regular replacement", replace: func(t testing.TB, token, saved string) func() {
			if err := os.Rename(token, saved); err != nil {
				t.Fatal(err)
			}
			writeTestToken(t, token, bearer)
			return func() {}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := privateTestDirectory(t)
			listener, socket := testListener(t, directory)
			defer listener.Close()
			token := filepath.Join(directory, "token")
			saved := filepath.Join(directory, "saved-token")
			writeTestToken(t, token, bearer)
			baselineFDs := countTestFDs(t)
			baselineGoroutines := runtime.NumGoroutine()
			var finish func()
			started := time.Now()
			record, err := loadTokenAtOpen(token, func() { finish = test.replace(t, token, saved) })
			if finish != nil {
				finish()
			}
			if !errors.Is(err, ErrInvalidClient) {
				t.Fatalf("replacement token record = %+v, error = %v", record, err)
			}
			if elapsed := time.Since(started); elapsed >= 150*time.Millisecond {
				t.Fatalf("replacement token open took %v, want less than 150ms", elapsed)
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), string(bearer[:])) {
				t.Fatalf("token error exposed private input: %v", err)
			}
			if err := listener.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if connection, acceptErr := listener.Accept(); acceptErr == nil {
				connection.Close()
				t.Fatal("token validation opened a connection")
			}
			if after := countTestFDs(t); after != baselineFDs {
				t.Fatalf("token replacement changed FD census: before=%d after=%d", baselineFDs, after)
			}
			if delta := runtime.NumGoroutine() - baselineGoroutines; delta > 1 {
				t.Fatalf("token replacement retained %d goroutines", delta)
			}
			_ = socket
		})
	}
}

func TestPrivateParentComponentSwapAtOpenIsRejected(t *testing.T) {
	for _, componentName := range []string{"outer", "inner"} {
		t.Run(componentName, func(t *testing.T) {
			base := privateTestDirectory(t)
			outer := filepath.Join(base, "outer")
			inner := filepath.Join(outer, "inner")
			if err := os.Mkdir(outer, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(inner, 0o700); err != nil {
				t.Fatal(err)
			}
			listener, socket := testListener(t, inner)
			defer listener.Close()
			token := filepath.Join(inner, "token")
			writeTestToken(t, token, testCredential('I'))
			target := map[string]string{"outer": outer, "inner": inner}[componentName]
			moved := target + "-moved"
			baselineFDs := countTestFDs(t)
			called := false
			root, _, err := openPrivateParentAt(token, func(component string) {
				if component != target {
					return
				}
				called = true
				if renameErr := os.Rename(target, moved); renameErr != nil {
					t.Fatal(renameErr)
				}
				if symlinkErr := os.Symlink(filepath.Base(moved), target); symlinkErr != nil {
					t.Fatal(symlinkErr)
				}
			}, nil)
			if root != nil {
				root.Close()
			}
			if !called || !errors.Is(err, ErrInvalidClient) {
				t.Fatalf("component swap called=%v error=%v", called, err)
			}
			if err := listener.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if connection, acceptErr := listener.Accept(); acceptErr == nil {
				connection.Close()
				t.Fatal("parent validation opened a connection")
			}
			if after := countTestFDs(t); after != baselineFDs {
				t.Fatalf("component swap changed FD census: before=%d after=%d", baselineFDs, after)
			}
			_ = socket
		})
	}
}

func TestPrivateParentAllowsUnrelatedChildCreation(t *testing.T) {
	base := privateTestDirectory(t)
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(outer, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Lstat(outer)
	if err != nil {
		t.Fatal(err)
	}
	before, ok := identityOf(beforeInfo)
	if !ok {
		t.Fatal("outer directory has no filesystem identity")
	}
	called := false
	root, _, err := openPrivateParentAt(filepath.Join(inner, "token"), nil, func(component string) {
		if component != outer {
			return
		}
		called = true
		if mkdirErr := os.Mkdir(filepath.Join(outer, "unrelated"), 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("unrelated child was not created during the directory check")
	}
	afterInfo, err := os.Lstat(outer)
	if err != nil {
		t.Fatal(err)
	}
	after, ok := identityOf(afterInfo)
	if !ok || before.same(after) || !before.sameDirectory(after) {
		t.Fatalf("outer identities before=%+v after=%+v", before, after)
	}
}

func TestPrivateParentOpenDoesNotFollowTransientComponent(t *testing.T) {
	base := privateTestDirectory(t)
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(outer, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(inner, "token")
	writeTestToken(t, token, testCredential('N'))
	moved := outer + "-moved"
	afterCalled := false
	root, _, err := openPrivateParentAt(token, func(component string) {
		if component != outer {
			return
		}
		if renameErr := os.Rename(outer, moved); renameErr != nil {
			t.Fatal(renameErr)
		}
		if symlinkErr := os.Symlink(filepath.Base(moved), outer); symlinkErr != nil {
			t.Fatal(symlinkErr)
		}
	}, func(component string) {
		if component != outer {
			return
		}
		afterCalled = true
		if removeErr := os.Remove(outer); removeErr != nil {
			t.Fatal(removeErr)
		}
		if renameErr := os.Rename(moved, outer); renameErr != nil {
			t.Fatal(renameErr)
		}
	})
	if root != nil {
		root.Close()
	}
	if afterCalled || !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("transient symlink was followed: after-open=%v error=%v", afterCalled, err)
	}
}

func TestPrivateRootTokenOpenDoesNotFollowSymlink(t *testing.T) {
	directory := privateTestDirectory(t)
	target := filepath.Join(directory, "target")
	writeTestToken(t, target, testCredential('L'))
	token := filepath.Join(directory, "token")
	if err := os.Symlink(filepath.Base(target), token); err != nil {
		t.Fatal(err)
	}
	root, _, err := openPrivateParent(token)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.openToken(filepath.Base(token))
	if file != nil {
		file.Close()
	}
	if err == nil {
		t.Fatal("descriptor-relative token open followed a symlink")
	}
}

func TestPrivateParentSymlinkSwapAcrossCallsFailsBeforeConnect(t *testing.T) {
	bearer := testCredential('C')
	base := privateTestDirectory(t)
	outer := filepath.Join(base, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(outer, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	listener, socket := testListener(t, inner)
	defer listener.Close()
	token := filepath.Join(inner, "token")
	writeTestToken(t, token, bearer)
	client, err := NewOperatorClient(socket, token)
	if err != nil {
		t.Fatal(err)
	}
	moved := outer + "-moved"
	if err := os.Rename(outer, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(moved), outer); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Health(context.Background()); !errors.Is(err, ErrInvalidClient) {
		t.Fatalf("parent symlink swap error = %v", err)
	}
	if err := listener.SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if connection, acceptErr := listener.Accept(); acceptErr == nil {
		connection.Close()
		t.Fatal("parent symlink swap opened a connection")
	}
}
