//go:build darwin

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"golang.org/x/sys/unix"
)

type localAPIFixture struct {
	homePath   string
	tokenPath  string
	runtimes   string
	socketPath string
	home       *OperationalHome
	authority  *LocalAPIAuthority
	protocol   *LocalAPIProtocol
}

func newLocalAPIFixture(t *testing.T, bearer byte) *localAPIFixture {
	t.Helper()
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(homePath, tokenName)
	if err := os.WriteFile(tokenPath, bytes.Repeat([]byte{bearer}, localAPITokenBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := home.OpenLocalAPI(context.Background())
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	protocol, err := authority.ClaimProtocol()
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	fixture := &localAPIFixture{
		homePath: homePath, tokenPath: tokenPath,
		runtimes:   filepath.Join(homePath, runtimesName),
		socketPath: filepath.Join(homePath, runtimesName, localAPISocketName),
		home:       home, authority: authority, protocol: protocol,
	}
	t.Cleanup(func() {
		if fixture.home != nil {
			_ = fixture.home.Close()
		}
	})
	return fixture
}

func (fixture *localAPIFixture) close(t *testing.T) {
	t.Helper()
	if fixture.home == nil {
		return
	}
	if err := fixture.home.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.home = nil
}

func waitForBlockedLocalAPIAccept(t *testing.T, authority *LocalAPIAuthority) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		authority.state.mu.Lock()
		accepting := authority.state.accepting
		authority.state.mu.Unlock()
		if accepting == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Accept never reached the owned listener")
		}
		time.Sleep(time.Millisecond)
	}
}

func acceptLocalAPIConnection(t *testing.T, fixture *localAPIFixture) (*LocalAPIConnection, *net.UnixConn) {
	t.Helper()
	accepted := make(chan *LocalAPIConnection, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := fixture.protocol.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fixture.socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case connection := <-accepted:
		return connection, client
	case err := <-acceptErr:
		_ = client.Close()
		t.Fatal(err)
	case <-time.After(time.Second):
		_ = client.Close()
		t.Fatal("local API connection was not accepted")
	}
	return nil, nil
}

func resetUncertainLocalAPIForCleanup(authority *LocalAPIAuthority, home *OperationalHome, listenerGone bool, socketGone bool) {
	authority.state.mu.Lock()
	authority.state.closed = false
	authority.state.closing = false
	authority.state.closeRunning = false
	authority.state.closeErr = nil
	authority.state.poisonErr = nil
	authority.state.cleanupErr = nil
	if listenerGone {
		authority.state.listener = nil
	}
	if socketGone {
		authority.state.socketID = identity{}
	}
	authority.state.mu.Unlock()
	home.state.mu.Lock()
	home.state.closed = false
	home.state.closeErr = nil
	home.state.mu.Unlock()
}

func TestLocalAPIAuthorityOwnsExactEndpointAndOperatorPrincipal(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'A')
	info, err := os.Lstat(fixture.socketPath)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || stat.Size != 0 {
		t.Fatalf("local API socket metadata = mode %v uid %d links %d size %d", info.Mode(), stat.Uid, stat.Nlink, stat.Size)
	}
	if err := fixture.protocol.Verify(); err != nil {
		t.Fatal(err)
	}
	if fixture.protocol.CheckOperator(bytes.Repeat([]byte{'B'}, localAPITokenBytes)) || fixture.protocol.CheckOperator([]byte("short")) {
		t.Fatal("wrong operator bearer was accepted")
	}
	if !fixture.protocol.CheckOperator(bytes.Repeat([]byte{'A'}, localAPITokenBytes)) {
		t.Fatal("correct bearer after wrong bearer was refused")
	}
	alias := *fixture.protocol
	if err := alias.Verify(); !errors.Is(err, ErrClosed) || alias.CheckOperator(bytes.Repeat([]byte{'A'}, localAPITokenBytes)) {
		t.Fatalf("copied protocol authority = Verify %v, operator %t", err, alias.CheckOperator(bytes.Repeat([]byte{'A'}, localAPITokenBytes)))
	}
	if connection, err := alias.Accept(); connection != nil || !errors.Is(err, ErrClosed) {
		t.Fatalf("copied protocol Accept = %v, %v", connection, err)
	}
	if dispatch, err := alias.BeginDispatch(); dispatch != nil || !errors.Is(err, ErrClosed) {
		t.Fatalf("copied protocol dispatch = %v, %v", dispatch, err)
	}
	lease, err := fixture.protocol.BeginDispatch()
	if err != nil {
		t.Fatal(err)
	}
	leaseAlias := *lease
	if err := leaseAlias.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("copied dispatch close = %v", err)
	}
	fixture.authority.state.mu.Lock()
	dispatching := fixture.authority.state.dispatching
	fixture.authority.state.mu.Unlock()
	if dispatching != 1 {
		t.Fatalf("copied dispatch released exact lease: dispatching=%d", dispatching)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if second, err := fixture.home.OpenLocalAPI(context.Background()); second != nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("second local API activation = %v, %v", second, err)
	}
	fixture.close(t)
	if _, err := os.Lstat(fixture.socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local API socket remains after home close: %v", err)
	}
	if err := fixture.protocol.Verify(); !errors.Is(err, ErrClosed) {
		t.Fatalf("authority after home close = %v", err)
	}
}

func TestLocalAPIAuthorityBindingMutationsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *localAPIFixture) func()
	}{
		{name: "token path replacement with same bytes", mutate: func(t *testing.T, fixture *localAPIFixture) func() {
			saved := fixture.tokenPath + ".saved"
			if err := os.Rename(fixture.tokenPath, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.tokenPath, bytes.Repeat([]byte{'M'}, localAPITokenBytes), 0o600); err != nil {
				t.Fatal(err)
			}
			return func() {
				_ = os.Remove(fixture.tokenPath)
				if err := os.Rename(saved, fixture.tokenPath); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "token in-place mutation", mutate: func(t *testing.T, fixture *localAPIFixture) func() {
			if err := os.WriteFile(fixture.tokenPath, bytes.Repeat([]byte{'X'}, localAPITokenBytes), 0o600); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.WriteFile(fixture.tokenPath, bytes.Repeat([]byte{'M'}, localAPITokenBytes), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "token append", mutate: func(t *testing.T, fixture *localAPIFixture) func() {
			file, err := os.OpenFile(fixture.tokenPath, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte("private-appended-token-sentinel")); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.WriteFile(fixture.tokenPath, bytes.Repeat([]byte{'M'}, localAPITokenBytes), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "runtimes path replacement", mutate: func(t *testing.T, fixture *localAPIFixture) func() {
			saved := fixture.runtimes + ".saved"
			if err := os.Rename(fixture.runtimes, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(fixture.runtimes, 0o700); err != nil {
				t.Fatal(err)
			}
			return func() {
				_ = os.Remove(fixture.runtimes)
				if err := os.Rename(saved, fixture.runtimes); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "socket path replacement", mutate: func(t *testing.T, fixture *localAPIFixture) func() {
			saved := fixture.socketPath + ".saved"
			if err := os.Rename(fixture.socketPath, saved); err != nil {
				t.Fatal(err)
			}
			replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: fixture.socketPath, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			replacement.SetUnlinkOnClose(false)
			return func() {
				if err := replacement.Close(); err != nil {
					t.Fatal(err)
				}
				_ = os.Remove(fixture.socketPath)
				if err := os.Rename(saved, fixture.socketPath); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{name: "whole home replacement", mutate: func(t *testing.T, fixture *localAPIFixture) func() {
			saved := fixture.homePath + ".saved"
			if err := os.Rename(fixture.homePath, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(fixture.homePath, 0o700); err != nil {
				t.Fatal(err)
			}
			return func() {
				_ = os.Remove(fixture.homePath)
				if err := os.Rename(saved, fixture.homePath); err != nil {
					t.Fatal(err)
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLocalAPIFixture(t, 'M')
			restore := test.mutate(t, fixture)
			if err := fixture.protocol.Verify(); !errors.Is(err, ErrUncertain) {
				t.Fatalf("mutated authority Verify = %v", err)
			}
			if fixture.protocol.CheckOperator(bytes.Repeat([]byte{'M'}, localAPITokenBytes)) {
				t.Fatal("mutated authority accepted operator bearer")
			}
			restore()
			if err := fixture.protocol.Verify(); !errors.Is(err, ErrUncertain) {
				t.Fatalf("restored poisoned authority revived = %v", err)
			}
			if fixture.protocol.CheckOperator(bytes.Repeat([]byte{'M'}, localAPITokenBytes)) {
				t.Fatal("restored poisoned authority revived operator access")
			}
			fixture.authority.state.mu.Lock()
			fixture.authority.state.poisonErr = nil
			fixture.authority.state.mu.Unlock()
			fixture.close(t)
		})
	}
}

func TestLocalAPIAuthorityRejectsPostOpenZeroPrincipalPermanently(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(homePath, tokenName)
	original, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	zero := make([]byte, operatorTokenBytes)
	if err := os.WriteFile(tokenPath, zero, 0o600); err != nil {
		t.Fatal(err)
	}
	authority, openErr := home.OpenLocalAPI(context.Background())
	if authority == nil || !errors.Is(openErr, ErrUncertain) || authority.state.poisonErr == nil {
		t.Fatalf("post-open zero principal = %v, %v", authority, openErr)
	}
	if protocol, err := authority.ClaimProtocol(); protocol != nil || !errors.Is(err, ErrUncertain) {
		t.Fatalf("zero principal protocol claim = %v, %v", protocol, err)
	}
	if authority.state.checkOperator(zero) || !errors.Is(authority.state.verify(), ErrUncertain) {
		t.Fatal("zero principal retained operator or attempt authority")
	}
	if retry, err := home.OpenLocalAPI(context.Background()); retry != nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("zero principal activation retry = %v, %v", retry, err)
	}
	if err := os.WriteFile(tokenPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if authority.state.checkOperator(original) || !errors.Is(authority.state.verify(), ErrUncertain) {
		t.Fatal("restoring a zero principal revived poisoned authority")
	}
	first := home.Close()
	if !errors.Is(first, ErrUncertain) || home.Close() != first {
		t.Fatalf("zero principal close result was not stable: %v", first)
	}
	if candidate, err := OpenOperationalHome(context.Background(), homePath); candidate != nil || !errors.Is(err, ErrBusy) {
		if candidate != nil {
			_ = candidate.Close()
		}
		t.Fatalf("zero principal uncertainty released home lease = %v, %v", candidate, err)
	}
	authority.state.mu.Lock()
	authority.state.digest = sha256.Sum256(original)
	authority.state.mu.Unlock()
	resetUncertainLocalAPIForCleanup(authority, home, true, true)
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAPIAuthorityRecoversOnlyExactRefusedStaleSocket(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := home.OpenLocalAPI(context.Background())
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	protocol, err := authority.ClaimProtocol()
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	after, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Sys().(*syscall.Stat_t).Ino == after.Sys().(*syscall.Stat_t).Ino || after.Mode().Perm() != 0o600 {
		t.Fatalf("stale socket was not replaced exactly: before=%v after=%v", before, after)
	}
	connection, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan error, 1)
	go func() {
		server, acceptErr := protocol.Accept()
		if acceptErr == nil {
			acceptErr = server.Close()
		}
		accepted <- acceptErr
	}()
	_ = connection.Close()
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAPIAuthorityRefusesLiveSocketAndCancelledActivationUntouched(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
	live, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	live.SetUnlinkOnClose(false)
	before, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	if authority, err := home.OpenLocalAPI(context.Background()); authority != nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("live socket activation = %v, %v", authority, err)
	}
	after, err := os.Lstat(socketPath)
	if err != nil || before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino {
		t.Fatalf("live socket changed: before=%v after=%v err=%v", before, after, err)
	}
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if authority, err := home.OpenLocalAPI(cancelled); authority != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled activation = %v, %v", authority, err)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled activation changed socket leaf: %v", err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAPIAuthorityRefusesAmbiguousStaleSocketWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		probe func(context.Context, string) (net.Conn, error)
	}{
		{name: "permission denied", probe: func(context.Context, string) (net.Conn, error) { return nil, unix.EACCES }},
		{name: "operation denied", probe: func(context.Context, string) (net.Conn, error) { return nil, unix.EPERM }},
		{name: "timeout", probe: func(ctx context.Context, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			homePath := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), homePath); err != nil {
				t.Fatal(err)
			}
			socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
			stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			stale.SetUnlinkOnClose(false)
			if err := stale.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(socketPath)
			if err != nil {
				t.Fatal(err)
			}
			home, err := OpenOperationalHome(context.Background(), homePath)
			if err != nil {
				t.Fatal(err)
			}
			original := localAPIDial
			localAPIDial = test.probe
			_, openErr := home.OpenLocalAPI(context.Background())
			localAPIDial = original
			if openErr == nil {
				t.Fatal("ambiguous stale socket was removed")
			}
			after, err := os.Lstat(socketPath)
			if err != nil || before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino {
				t.Fatalf("ambiguous stale socket changed: before=%v after=%v err=%v", before, after, err)
			}
			if err := home.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalAPIAuthorityChangedStaleIdentityAndMalformedLeafRemain(t *testing.T) {
	t.Run("changed during probe", func(t *testing.T) {
		parent := installTempDir(t)
		homePath := filepath.Join(parent, "home")
		if _, err := Init(context.Background(), homePath); err != nil {
			t.Fatal(err)
		}
		socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
		makeStale := func() {
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			listener.SetUnlinkOnClose(false)
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
		}
		makeStale()
		home, err := OpenOperationalHome(context.Background(), homePath)
		if err != nil {
			t.Fatal(err)
		}
		original := localAPIDial
		localAPIDial = func(context.Context, string) (net.Conn, error) {
			if err := os.Rename(socketPath, socketPath+".first"); err != nil {
				t.Fatal(err)
			}
			makeStale()
			return nil, unix.ECONNREFUSED
		}
		_, openErr := home.OpenLocalAPI(context.Background())
		localAPIDial = original
		if openErr == nil {
			t.Fatal("changed stale socket was accepted")
		}
		if _, err := os.Lstat(socketPath); err != nil {
			t.Fatalf("replacement stale socket was removed: %v", err)
		}
		if err := home.Close(); err != nil {
			t.Fatal(err)
		}
	})

	for _, kind := range []string{"regular", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := installTempDir(t)
			homePath := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), homePath); err != nil {
				t.Fatal(err)
			}
			socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
			switch kind {
			case "regular":
				if err := os.WriteFile(socketPath, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(socketPath, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("missing", socketPath); err != nil {
					t.Fatal(err)
				}
			}
			home, err := OpenOperationalHome(context.Background(), homePath)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := home.OpenLocalAPI(context.Background()); got != nil || err == nil {
				t.Fatalf("malformed leaf activation = %v, %v", got, err)
			}
			if _, err := os.Lstat(socketPath); err != nil {
				t.Fatalf("malformed leaf changed: %v", err)
			}
			if err := home.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalAPIAuthorityActivationFaultsCleanExactSocketAndPermitRetry(t *testing.T) {
	for _, phase := range []string{"after bind", "before chmod", "after chmod", "after record", "after sync"} {
		t.Run(phase, func(t *testing.T) {
			parent := installTempDir(t)
			homePath := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), homePath); err != nil {
				t.Fatal(err)
			}
			home, err := OpenOperationalHome(context.Background(), homePath)
			if err != nil {
				t.Fatal(err)
			}
			original := localAPIPhaseHook
			localAPIPhaseHook = func(current string) error {
				if current == phase {
					return errors.New("injected local API activation fault")
				}
				return nil
			}
			if got, err := home.OpenLocalAPI(context.Background()); got != nil || err == nil || errors.Is(err, ErrUncertain) {
				localAPIPhaseHook = original
				t.Fatalf("faulted activation = %v, %v", got, err)
			}
			localAPIPhaseHook = original
			socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
			if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("faulted activation retained socket: %v", err)
			}
			authority, err := home.OpenLocalAPI(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if authority == nil {
				t.Fatal("retry returned nil authority")
			}
			if err := home.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalAPIAuthorityObservesChmodAndSyncFailuresAtRealEffects(t *testing.T) {
	tests := []struct {
		name    string
		install func() func()
	}{
		{name: "chmod", install: func() func() {
			original := localAPIChmod
			failed := false
			localAPIChmod = func(parent int, name string, mode uint32) error {
				if !failed {
					failed = true
					return unix.EIO
				}
				return original(parent, name, mode)
			}
			return func() { localAPIChmod = original }
		}},
		{name: "directory fsync", install: func() func() {
			original := localAPISyncDirectory
			failed := false
			localAPISyncDirectory = func(fd int) error {
				if !failed {
					failed = true
					return unix.EIO
				}
				return original(fd)
			}
			return func() { localAPISyncDirectory = original }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := installTempDir(t)
			homePath := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), homePath); err != nil {
				t.Fatal(err)
			}
			home, err := OpenOperationalHome(context.Background(), homePath)
			if err != nil {
				t.Fatal(err)
			}
			restore := test.install()
			if authority, err := home.OpenLocalAPI(context.Background()); authority != nil || err == nil || errors.Is(err, ErrUncertain) {
				restore()
				t.Fatalf("effect failure activation = %v, %v", authority, err)
			}
			restore()
			socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
			if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("effect failure retained socket: %v", err)
			}
			if authority, err := home.OpenLocalAPI(context.Background()); authority == nil || err != nil {
				t.Fatalf("activation retry = %v, %v", authority, err)
			}
			if err := home.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalAPIAuthorityOwnsActivationProbeDeadlinesAndConnections(t *testing.T) {
	t.Run("successful proof closes both connections", func(t *testing.T) {
		parent := installTempDir(t)
		homePath := filepath.Join(parent, "home")
		if _, err := Init(context.Background(), homePath); err != nil {
			t.Fatal(err)
		}
		home, err := OpenOperationalHome(context.Background(), homePath)
		if err != nil {
			t.Fatal(err)
		}
		original := localAPICloseProbeConnection
		closed := 0
		localAPICloseProbeConnection = func(connection net.Conn) error {
			closed++
			return original(connection)
		}
		authority, openErr := home.OpenLocalAPI(context.Background())
		localAPICloseProbeConnection = original
		if openErr != nil || authority == nil || closed != 2 {
			t.Fatalf("activation probe ownership = %v, %v, closes=%d", authority, openErr, closed)
		}
		if err := home.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("probe close uncertainty", func(t *testing.T) {
		parent := installTempDir(t)
		homePath := filepath.Join(parent, "home")
		if _, err := Init(context.Background(), homePath); err != nil {
			t.Fatal(err)
		}
		home, err := OpenOperationalHome(context.Background(), homePath)
		if err != nil {
			t.Fatal(err)
		}
		original := localAPICloseProbeConnection
		failed := false
		localAPICloseProbeConnection = func(connection net.Conn) error {
			if !failed {
				failed = true
				return unix.EIO
			}
			return original(connection)
		}
		authority, openErr := home.OpenLocalAPI(context.Background())
		localAPICloseProbeConnection = original
		if authority == nil || !errors.Is(openErr, ErrUncertain) || len(authority.state.probeConnections) != 1 {
			t.Fatalf("probe-close uncertainty = %v, %v, retained=%d", authority, openErr, len(authority.state.probeConnections))
		}
		if retry, err := home.OpenLocalAPI(context.Background()); retry != nil || !errors.Is(err, ErrBusy) {
			t.Fatalf("probe-close retry = %v, %v", retry, err)
		}
		for _, connection := range authority.state.probeConnections {
			if err := original(connection); err != nil && !errors.Is(err, net.ErrClosed) {
				t.Fatal(err)
			}
		}
		authority.state.probeConnections = nil
		resetUncertainLocalAPIForCleanup(authority, home, true, true)
		if err := home.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("deadline reset uncertainty is sanitized", func(t *testing.T) {
		parent := installTempDir(t)
		homePath := filepath.Join(parent, "private-deadline-path-sentinel-home")
		if _, err := Init(context.Background(), homePath); err != nil {
			t.Fatal(err)
		}
		home, err := OpenOperationalHome(context.Background(), homePath)
		if err != nil {
			t.Fatal(err)
		}
		original := localAPISetListenerDeadline
		localAPISetListenerDeadline = func(listener *net.UnixListener, deadline time.Time) error {
			if deadline.IsZero() {
				return errors.New("private-deadline-effect-sentinel")
			}
			return original(listener, deadline)
		}
		authority, openErr := home.OpenLocalAPI(context.Background())
		localAPISetListenerDeadline = original
		if authority == nil || !errors.Is(openErr, ErrUncertain) {
			t.Fatalf("deadline-reset uncertainty = %v, %v", authority, openErr)
		}
		if strings.Contains(openErr.Error(), homePath) || strings.Contains(openErr.Error(), "private-deadline-effect-sentinel") {
			t.Fatalf("deadline error exposed private detail: %v", openErr)
		}
		resetUncertainLocalAPIForCleanup(authority, home, true, true)
		if err := home.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestLocalAPIAuthorityStaleProbeCloseUncertaintyRetainsExactOwner(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
	live, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	live.SetUnlinkOnClose(false)
	before, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	original := localAPICloseProbeConnection
	localAPICloseProbeConnection = func(net.Conn) error { return unix.EIO }
	authority, openErr := home.OpenLocalAPI(context.Background())
	localAPICloseProbeConnection = original
	if authority == nil || !errors.Is(openErr, ErrUncertain) || len(authority.state.probeConnections) != 1 {
		t.Fatalf("stale probe-close uncertainty = %v, %v", authority, openErr)
	}
	after, err := os.Lstat(socketPath)
	if err != nil || before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino {
		t.Fatalf("stale probe uncertainty changed live socket: before=%v after=%v err=%v", before, after, err)
	}
	if candidate, err := OpenOperationalHome(context.Background(), homePath); candidate != nil || !errors.Is(err, ErrBusy) {
		if candidate != nil {
			_ = candidate.Close()
		}
		t.Fatalf("stale probe uncertainty released lease = %v, %v", candidate, err)
	}
	for _, connection := range authority.state.probeConnections {
		if err := original(connection); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	}
	authority.state.probeConnections = nil
	if err := live.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socketPath); err != nil {
		t.Fatal(err)
	}
	resetUncertainLocalAPIForCleanup(authority, home, true, true)
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAPIAuthorityStaleProbeConnectionOverridesDialError(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	probe, peer := net.Pipe()
	originalDial, originalClose := localAPIDial, localAPICloseProbeConnection
	closed := 0
	localAPIDial = func(context.Context, string) (net.Conn, error) {
		return probe, unix.ECONNREFUSED
	}
	localAPICloseProbeConnection = func(connection net.Conn) error {
		if connection != probe {
			t.Fatalf("stale probe closed foreign connection %T", connection)
		}
		closed++
		return originalClose(connection)
	}
	authority, openErr := home.OpenLocalAPI(context.Background())
	localAPIDial, localAPICloseProbeConnection = originalDial, originalClose
	_ = peer.Close()
	if authority != nil || !errors.Is(openErr, ErrBusy) || closed != 1 {
		t.Fatalf("connection-plus-error probe = %v, %v, closes=%d", authority, openErr, closed)
	}
	after, err := os.Lstat(socketPath)
	if err != nil || before.Sys().(*syscall.Stat_t).Ino != after.Sys().(*syscall.Stat_t).Ino {
		t.Fatalf("connection-plus-error probe changed socket: before=%v after=%v err=%v", before, after, err)
	}
	authority, openErr = home.OpenLocalAPI(context.Background())
	if authority == nil || openErr != nil {
		t.Fatalf("retry after positive probe = %v, %v", authority, openErr)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAPIAuthorityBindRaceAndRecordReplacementFailClosed(t *testing.T) {
	t.Run("bind race preserves live peer", func(t *testing.T) {
		parent := installTempDir(t)
		homePath := filepath.Join(parent, "home")
		if _, err := Init(context.Background(), homePath); err != nil {
			t.Fatal(err)
		}
		home, err := OpenOperationalHome(context.Background(), homePath)
		if err != nil {
			t.Fatal(err)
		}
		socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
		var intruder *net.UnixListener
		original := localAPIPhaseHook
		localAPIPhaseHook = func(phase string) error {
			if phase != "before bind" {
				return nil
			}
			var err error
			intruder, err = net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
			return err
		}
		authority, openErr := home.OpenLocalAPI(context.Background())
		if authority != nil || openErr == nil {
			localAPIPhaseHook = original
			t.Fatalf("bind race activation = %v, %v", authority, openErr)
		}
		localAPIPhaseHook = original
		if strings.Contains(openErr.Error(), socketPath) || strings.Contains(openErr.Error(), homePath) {
			t.Fatalf("bind error exposed private locator: %v", openErr)
		}
		if intruder == nil {
			t.Fatal("bind-race peer was not created")
		}
		if _, err := os.Lstat(socketPath); err != nil {
			t.Fatalf("bind-race peer was removed: %v", err)
		}
		if err := intruder.Close(); err != nil {
			t.Fatal(err)
		}
		if err := home.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("record replacement by another socket is never adopted", func(t *testing.T) {
		parent := installTempDir(t)
		homePath := filepath.Join(parent, "home")
		if _, err := Init(context.Background(), homePath); err != nil {
			t.Fatal(err)
		}
		home, err := OpenOperationalHome(context.Background(), homePath)
		if err != nil {
			t.Fatal(err)
		}
		socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
		owned := socketPath + ".owned"
		var intruder *net.UnixListener
		original := localAPIPhaseHook
		localAPIPhaseHook = func(phase string) error {
			if phase != "before record" {
				return nil
			}
			if err := os.Rename(socketPath, owned); err != nil {
				return err
			}
			var err error
			intruder, err = net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
			if intruder != nil {
				intruder.SetUnlinkOnClose(false)
			}
			return err
		}
		authority, openErr := home.OpenLocalAPI(context.Background())
		localAPIPhaseHook = original
		if authority == nil || !errors.Is(openErr, ErrUncertain) {
			t.Fatalf("record replacement activation = %v, %v", authority, openErr)
		}
		if intruder == nil {
			t.Fatal("record intruder socket was not created")
		}
		if info, err := os.Lstat(socketPath); err != nil || info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("record intruder changed: %v, %v", info, err)
		}
		if retry, err := home.OpenLocalAPI(context.Background()); retry != nil || !errors.Is(err, ErrBusy) {
			t.Fatalf("retained-uncertain activation retry = %v, %v", retry, err)
		}

		if err := intruder.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(socketPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(owned); err != nil {
			t.Fatal(err)
		}
		resetUncertainLocalAPIForCleanup(authority, home, true, true)
		if err := home.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestLocalAPIAuthorityInvalidLocatorErrorIsSanitized(t *testing.T) {
	parent := installTempDir(t)
	privateSegment := strings.Repeat("private-locator-sentinel-", 5)
	if err := os.Mkdir(filepath.Join(parent, privateSegment), 0o700); err != nil {
		t.Fatal(err)
	}
	homePath := filepath.Join(parent, privateSegment, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	authority, openErr := home.OpenLocalAPI(context.Background())
	if authority != nil || !errors.Is(openErr, ErrInvalidHome) {
		t.Fatalf("long locator activation = %v, %v", authority, openErr)
	}
	if strings.Contains(openErr.Error(), privateSegment) || strings.Contains(openErr.Error(), homePath) {
		t.Fatalf("invalid locator error exposed private path: %v", openErr)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAPIAuthorityCloseJoinsAcceptAndConnectionsWithoutHomeMutex(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'J')
	accepted := make(chan *LocalAPIConnection, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := fixture.protocol.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fixture.socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	var server *LocalAPIConnection
	select {
	case server = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("accept did not complete")
	}
	closed := make(chan error, 1)
	go func() { closed <- fixture.home.Close() }()
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if count, err := client.Read(make([]byte, 1)); count != 0 || err == nil {
		t.Fatalf("home close did not close accepted transport: count=%d err=%v", count, err)
	}
	select {
	case err := <-closed:
		t.Fatalf("home close returned before connection owner joined: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("home close deadlocked after exact connection release")
	}
	fixture.home = nil
	_ = client.Close()
}

func TestOperationalHomeCloseQuarantinesUncertainConnectionAndClosesStore(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'U')
	store, err := fixture.home.OpenStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server, client := acceptLocalAPIConnection(t, fixture)
	lease, err := fixture.protocol.BeginDispatch()
	if err != nil {
		t.Fatal(err)
	}
	originalClose := localAPICloseConnection
	localAPICloseConnection = func(*net.UnixConn) error { return unix.EIO }
	closed := make(chan error, 1)
	go func() { closed <- fixture.home.Close() }()

	quarantined := false
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		fixture.authority.state.mu.Lock()
		quarantined = len(fixture.authority.state.connections) == 0 && len(fixture.authority.state.quarantined) == 1
		fixture.authority.state.mu.Unlock()
		if quarantined {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !quarantined {
		localAPICloseConnection = originalClose
		_ = fixture.authority.state.closeExactConnection(server, true)
		_ = lease.Close()
		<-closed
		t.Fatal("uncertain connection close remained in the active join census")
	}
	localAPICloseConnection = originalClose
	if next, err := fixture.protocol.BeginDispatch(); next != nil || !errors.Is(err, ErrClosed) {
		t.Fatalf("dispatch after home close began = %v, %v", next, err)
	}
	if _, err := store.Factory(context.Background()); err != nil {
		t.Fatalf("entered dispatch lost Store before release: %v", err)
	}
	select {
	case err := <-closed:
		t.Fatalf("home close passed entered dispatch: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	var first error
	select {
	case first = <-closed:
	case <-time.After(time.Second):
		t.Fatal("home close waited for a quarantined connection owner")
	}
	if !errors.Is(first, ErrUncertain) || fixture.home.Close() != first {
		t.Fatalf("uncertain connection home result was not stable: %v", first)
	}
	if _, err := store.Factory(context.Background()); !errors.Is(err, kernel.ErrStoreClosed) {
		t.Fatalf("Store remained usable after uncertain connection close: %v", err)
	}
	if fixture.authority.state.token == nil || fixture.authority.state.runtimes == nil {
		t.Fatal("uncertain connection close released Local API authority")
	}
	if candidate, err := OpenOperationalHome(context.Background(), fixture.homePath); candidate != nil || !errors.Is(err, ErrBusy) {
		if candidate != nil {
			_ = candidate.Close()
		}
		t.Fatalf("uncertain connection close released home lease = %v, %v", candidate, err)
	}
	if err := server.Close(); err != nil {
		t.Fatalf("exact quarantined owner retry = %v", err)
	}
	fixture.authority.state.mu.Lock()
	remaining := len(fixture.authority.state.quarantined)
	fixture.authority.state.mu.Unlock()
	if remaining != 0 || fixture.home.Close() != first {
		t.Fatalf("quarantined owner retry changed stable home result: remaining=%d result=%v", remaining, fixture.home.Close())
	}
	_ = client.Close()
	resetUncertainLocalAPIForCleanup(fixture.authority, fixture.home, true, false)
	fixture.close(t)
}

func TestLocalAPIAuthorityHomeCloseUnblocksBlockedAccept(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'B')
	acceptDone := make(chan error, 1)
	go func() {
		connection, err := fixture.protocol.Accept()
		if connection != nil {
			err = errors.Join(err, connection.Close())
		}
		acceptDone <- err
	}()
	waitForBlockedLocalAPIAccept(t, fixture.authority)
	closed := make(chan error, 1)
	go func() { closed <- fixture.home.Close() }()
	select {
	case err := <-acceptDone:
		if err == nil {
			t.Fatal("blocked Accept succeeded during home close")
		}
	case <-time.After(time.Second):
		t.Fatal("home close did not unblock blocked Accept")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("home close deadlocked with blocked Accept")
	}
	fixture.home = nil
}

func TestLocalAPIConnectionRefusesForeignClose(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'F')
	path := filepath.Join(filepath.Dir(fixture.socketPath), "foreign.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	accepted := make(chan *net.UnixConn, 1)
	go func() {
		connection, _ := listener.AcceptUnix()
		accepted <- connection
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	server := <-accepted
	foreign := &LocalAPIConnection{state: &localAPIConnectionState{owner: fixture.authority.state, raw: server}}
	if err := foreign.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("foreign connection close = %v", err)
	}
	if _, err := client.Write([]byte{'x'}); err != nil {
		t.Fatalf("foreign transport was closed: %v", err)
	}
	if err := server.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if count, err := server.Read(buffer); count != 1 || err != nil || buffer[0] != 'x' {
		t.Fatalf("foreign transport read = %q, %d, %v", buffer, count, err)
	}
	_ = client.Close()
	_ = server.Close()
	_ = listener.Close()
	_ = os.Remove(path)
	fixture.close(t)
}

func TestLocalAPIConnectionCopyCannotConsumeExactOwner(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'C')
	accepted := make(chan *LocalAPIConnection, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := fixture.protocol.Accept()
		if err != nil {
			acceptErr <- err
			return
		}
		accepted <- connection
	}()
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: fixture.socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	var connection *LocalAPIConnection
	select {
	case connection = <-accepted:
	case err := <-acceptErr:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("exact connection was not accepted")
	}
	alias := *connection
	if err := alias.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("copied connection close = %v", err)
	}
	if _, err := client.Write([]byte{'x'}); err != nil {
		t.Fatalf("copied connection closed exact transport: %v", err)
	}
	if err := connection.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if count, err := connection.Read(buffer); count != 1 || err != nil || buffer[0] != 'x' {
		t.Fatalf("exact connection after copied close = %q, %d, %v", buffer, count, err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	fixture.close(t)
}

func TestLocalAPIAuthoritySocketReplacementClosePreservesIntruderAndRetainsLease(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'U')
	moved := fixture.socketPath + ".owned"
	if err := os.Rename(fixture.socketPath, moved); err != nil {
		t.Fatal(err)
	}
	intruder := []byte("private-intruder-sentinel")
	if err := os.WriteFile(fixture.socketPath, intruder, 0o600); err != nil {
		t.Fatal(err)
	}
	first := fixture.home.Close()
	if !errors.Is(first, ErrUncertain) {
		t.Fatalf("home close after socket replacement = %v", first)
	}
	if second := fixture.home.Close(); second != first {
		t.Fatalf("repeated uncertain close = %v, want stable %v", second, first)
	}
	if got, err := os.ReadFile(fixture.socketPath); err != nil || !bytes.Equal(got, intruder) {
		t.Fatalf("intruder changed: %q, %v", got, err)
	}
	if candidate, err := OpenOperationalHome(context.Background(), fixture.homePath); candidate != nil || !errors.Is(err, ErrBusy) {
		if candidate != nil {
			_ = candidate.Close()
		}
		t.Fatalf("home lease after uncertain child close = %v, %v", candidate, err)
	}
	if formatted := first.Error(); strings.Contains(formatted, string(bytes.Repeat([]byte{'U'}, localAPITokenBytes))) || strings.Contains(formatted, string(intruder)) {
		t.Fatalf("uncertain error exposed private sentinel: %v", first)
	}

	// Restore the exact namespace and reset only the package-local close state
	// so test cleanup can release descriptors; production never retries an
	// uncertain close.
	if err := os.Remove(fixture.socketPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, fixture.socketPath); err != nil {
		t.Fatal(err)
	}
	fixture.authority.state.mu.Lock()
	fixture.authority.state.closed = false
	fixture.authority.state.closing = false
	fixture.authority.state.closeRunning = false
	fixture.authority.state.closeErr = nil
	fixture.authority.state.mu.Unlock()
	fixture.home.state.mu.Lock()
	fixture.home.state.closed = false
	fixture.home.state.closeErr = nil
	fixture.home.state.mu.Unlock()
	fixture.close(t)
}

func TestLocalAPIAuthorityWholeHomeReplacementCloseUsesRetainedParent(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'P')
	movedHome := fixture.homePath + ".owned"
	if err := os.Rename(fixture.homePath, movedHome); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.homePath, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementRuntimes := filepath.Join(fixture.homePath, runtimesName)
	if err := os.Mkdir(replacementRuntimes, 0o700); err != nil {
		t.Fatal(err)
	}
	intruderPath := filepath.Join(replacementRuntimes, localAPISocketName)
	intruder, err := net.ListenUnix("unix", &net.UnixAddr{Name: intruderPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	intruder.SetUnlinkOnClose(false)
	if err := fixture.authority.Close(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("authority close after whole-home replacement = %v", err)
	}
	if _, err := os.Lstat(intruderPath); err != nil {
		t.Fatalf("replacement-home intruder was removed: %v", err)
	}
	ownedSocket := filepath.Join(movedHome, runtimesName, localAPISocketName)
	if _, err := os.Lstat(ownedSocket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retained-parent owned socket remains: %v", err)
	}
	if err := intruder.Close(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(intruderPath)
	if err := os.RemoveAll(fixture.homePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(movedHome, fixture.homePath); err != nil {
		t.Fatal(err)
	}
	fixture.authority.state.mu.Lock()
	fixture.authority.state.closed = false
	fixture.authority.state.closing = false
	fixture.authority.state.closeRunning = false
	fixture.authority.state.closeErr = nil
	fixture.authority.state.listener = nil
	fixture.authority.state.socketID = identity{}
	fixture.authority.state.mu.Unlock()
	fixture.home.state.mu.Lock()
	fixture.home.state.closed = false
	fixture.home.state.closeErr = nil
	fixture.home.state.mu.Unlock()
	fixture.close(t)
}

func TestLocalAPIAuthorityStaleRemovalDurabilityUncertaintyRetainsLease(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	originalSync := localAPISyncDirectory
	failed := false
	localAPISyncDirectory = func(fd int) error {
		if !failed {
			failed = true
			return unix.EIO
		}
		return originalSync(fd)
	}
	authority, openErr := home.OpenLocalAPI(context.Background())
	localAPISyncDirectory = originalSync
	if authority == nil || !errors.Is(openErr, ErrUncertain) {
		t.Fatalf("stale-removal fsync result = %v, %v", authority, openErr)
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale socket removal did not occur before fsync uncertainty: %v", err)
	}
	if retry, err := home.OpenLocalAPI(context.Background()); retry != nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("retry after stale-removal uncertainty = %v, %v", retry, err)
	}
	first := home.Close()
	if !errors.Is(first, ErrUncertain) || home.Close() != first {
		t.Fatalf("stale-removal close result was not stable: %v", first)
	}
	if candidate, err := OpenOperationalHome(context.Background(), homePath); candidate != nil || !errors.Is(err, ErrBusy) {
		if candidate != nil {
			_ = candidate.Close()
		}
		t.Fatalf("stale-removal uncertainty released lease = %v, %v", candidate, err)
	}
	resetUncertainLocalAPIForCleanup(authority, home, true, true)
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalAPIAuthorityStaleUnlinkErrorsRetainAmbiguousOwner(t *testing.T) {
	for _, unlinkErr := range []error{unix.EIO, unix.EINTR} {
		t.Run(unlinkErr.Error(), func(t *testing.T) {
			parent := installTempDir(t)
			homePath := filepath.Join(parent, "home")
			if _, err := Init(context.Background(), homePath); err != nil {
				t.Fatal(err)
			}
			socketPath := filepath.Join(homePath, runtimesName, localAPISocketName)
			stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			stale.SetUnlinkOnClose(false)
			if err := stale.Close(); err != nil {
				t.Fatal(err)
			}
			home, err := OpenOperationalHome(context.Background(), homePath)
			if err != nil {
				t.Fatal(err)
			}
			original := localAPIUnlinkAt
			calls := 0
			localAPIUnlinkAt = func(parent int, name string, flags int) error {
				calls++
				if err := original(parent, name, flags); err != nil {
					return err
				}
				return unlinkErr
			}
			authority, openErr := home.OpenLocalAPI(context.Background())
			localAPIUnlinkAt = original
			if authority == nil || !errors.Is(openErr, ErrUncertain) || calls != 1 {
				t.Fatalf("ambiguous stale unlink = %v, %v, calls=%d", authority, openErr, calls)
			}
			if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ambiguous unlink fixture did not exercise removed postcondition: %v", err)
			}
			if authority.state.listener != nil || authority.state.token == nil || authority.state.runtimes == nil {
				t.Fatal("ambiguous stale unlink rebound or released exact authority")
			}
			if retry, err := home.OpenLocalAPI(context.Background()); retry != nil || !errors.Is(err, ErrBusy) {
				t.Fatalf("retry after ambiguous stale unlink = %v, %v", retry, err)
			}
			first := home.Close()
			if !errors.Is(first, ErrUncertain) || home.Close() != first {
				t.Fatalf("ambiguous stale unlink result was not stable: %v", first)
			}
			if candidate, err := OpenOperationalHome(context.Background(), homePath); candidate != nil || !errors.Is(err, ErrBusy) {
				if candidate != nil {
					_ = candidate.Close()
				}
				t.Fatalf("ambiguous stale unlink released home lease = %v, %v", candidate, err)
			}
			resetUncertainLocalAPIForCleanup(authority, home, true, true)
			if err := home.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalAPIAuthorityRetainsEveryUncertainCloseBoundary(t *testing.T) {
	t.Run("rejected activation descriptor closes", func(t *testing.T) {
		parent := installTempDir(t)
		homePath := filepath.Join(parent, "home")
		if _, err := Init(context.Background(), homePath); err != nil {
			t.Fatal(err)
		}
		home, err := OpenOperationalHome(context.Background(), homePath)
		if err != nil {
			t.Fatal(err)
		}
		originalPhase, originalClose := localAPIPhaseHook, localAPICloseFile
		localAPIPhaseHook = func(phase string) error {
			if phase == "before bind" {
				return errors.New("injected activation failure")
			}
			return nil
		}
		localAPICloseFile = func(*os.File) error { return unix.EIO }
		authority, openErr := home.OpenLocalAPI(context.Background())
		localAPIPhaseHook, localAPICloseFile = originalPhase, originalClose
		if authority == nil || !errors.Is(openErr, ErrUncertain) || authority.state.token == nil || authority.state.runtimes == nil {
			t.Fatalf("descriptor-close uncertainty = %v, %v", authority, openErr)
		}
		first := home.Close()
		if !errors.Is(first, ErrUncertain) || home.Close() != first {
			t.Fatalf("descriptor-close result was not stable: %v", first)
		}
		if candidate, err := OpenOperationalHome(context.Background(), homePath); candidate != nil || !errors.Is(err, ErrBusy) {
			if candidate != nil {
				_ = candidate.Close()
			}
			t.Fatalf("descriptor uncertainty released lease = %v, %v", candidate, err)
		}
		resetUncertainLocalAPIForCleanup(authority, home, true, true)
		if err := home.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("listener close", func(t *testing.T) {
		fixture := newLocalAPIFixture(t, 'L')
		acceptDone := make(chan error, 1)
		go func() {
			connection, err := fixture.protocol.Accept()
			if connection != nil {
				err = errors.Join(err, connection.Close())
			}
			acceptDone <- err
		}()
		waitForBlockedLocalAPIAccept(t, fixture.authority)
		original := localAPICloseListener
		localAPICloseListener = func(*net.UnixListener) error { return unix.EIO }
		closed := make(chan error, 1)
		go func() { closed <- fixture.home.Close() }()
		var first error
		select {
		case first = <-closed:
		case <-time.After(time.Second):
			localAPICloseListener = original
			t.Fatal("listener-close uncertainty stranded blocked Accept")
		}
		localAPICloseListener = original
		select {
		case err := <-acceptDone:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("blocked Accept after listener close fault = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("blocked Accept owner did not join")
		}
		if !errors.Is(first, ErrUncertain) || fixture.home.Close() != first {
			t.Fatalf("listener-close result was not stable: %v", first)
		}
		if fixture.authority.state.token == nil || fixture.authority.state.runtimes == nil {
			t.Fatal("listener-close uncertainty released child descriptors")
		}
		if candidate, err := OpenOperationalHome(context.Background(), fixture.homePath); candidate != nil || !errors.Is(err, ErrBusy) {
			if candidate != nil {
				_ = candidate.Close()
			}
			t.Fatalf("listener uncertainty released lease = %v, %v", candidate, err)
		}
		resetUncertainLocalAPIForCleanup(fixture.authority, fixture.home, false, false)
		fixture.close(t)
	})
}

func TestOperationalHomeCloseRevokesStoreDespiteLocalAPIUncertainty(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'S')
	store, err := fixture.home.OpenStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	owned := fixture.socketPath + ".owned"
	if err := os.Rename(fixture.socketPath, owned); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.socketPath, []byte("store-close-intruder"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := fixture.home.Close()
	if !errors.Is(first, ErrUncertain) {
		t.Fatalf("combined child close = %v", first)
	}
	if _, err := store.Factory(context.Background()); !errors.Is(err, kernel.ErrStoreClosed) {
		t.Fatalf("Store remained usable after home close uncertainty: %v", err)
	}
	if candidate, err := OpenOperationalHome(context.Background(), fixture.homePath); candidate != nil || !errors.Is(err, ErrBusy) {
		if candidate != nil {
			_ = candidate.Close()
		}
		t.Fatalf("combined child uncertainty released home lease = %v, %v", candidate, err)
	}
	if err := os.Remove(fixture.socketPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(owned, fixture.socketPath); err != nil {
		t.Fatal(err)
	}
	resetUncertainLocalAPIForCleanup(fixture.authority, fixture.home, false, false)
	fixture.close(t)
}

func TestLocalAPIAuthorityConcurrentVerifyAndCloseConverge(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'V')
	start := make(chan struct{})
	results := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			for range 100 {
				err := fixture.protocol.Verify()
				if err != nil && !errors.Is(err, ErrClosed) {
					results <- err
					return
				}
			}
			results <- nil
		}()
	}
	close(start)
	if err := fixture.home.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.home = nil
	for range 8 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Verify result = %v", err)
		}
	}
	if err := fixture.protocol.Verify(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Verify after close = %v", err)
	}
}
