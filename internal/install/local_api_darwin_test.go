//go:build darwin

package install

import (
	"bytes"
	"context"
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
	fixture := &localAPIFixture{
		homePath: homePath, tokenPath: tokenPath,
		runtimes:   filepath.Join(homePath, runtimesName),
		socketPath: filepath.Join(homePath, runtimesName, localAPISocketName),
		home:       home, authority: authority,
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

func resetUncertainLocalAPIForCleanup(authority *LocalAPIAuthority, home *OperationalHome, listenerGone bool, socketGone bool) {
	authority.state.mu.Lock()
	authority.state.closed = false
	authority.state.closing = false
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
	if err := fixture.authority.Verify(); err != nil {
		t.Fatal(err)
	}
	if !fixture.authority.CheckOperator(bytes.Repeat([]byte{'A'}, localAPITokenBytes)) {
		t.Fatal("exact operator bearer was refused")
	}
	if fixture.authority.CheckOperator(bytes.Repeat([]byte{'B'}, localAPITokenBytes)) || fixture.authority.CheckOperator([]byte("short")) {
		t.Fatal("wrong operator bearer was accepted")
	}
	if second, err := fixture.home.OpenLocalAPI(context.Background()); second != nil || !errors.Is(err, ErrBusy) {
		t.Fatalf("second local API activation = %v, %v", second, err)
	}
	fixture.close(t)
	if _, err := os.Lstat(fixture.socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local API socket remains after home close: %v", err)
	}
	if err := fixture.authority.Verify(); !errors.Is(err, ErrClosed) {
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
			if err := fixture.authority.Verify(); !errors.Is(err, ErrUncertain) {
				t.Fatalf("mutated authority Verify = %v", err)
			}
			if fixture.authority.CheckOperator(bytes.Repeat([]byte{'M'}, localAPITokenBytes)) {
				t.Fatal("mutated authority accepted operator bearer")
			}
			restore()
			if err := fixture.authority.Verify(); !errors.Is(err, ErrUncertain) {
				t.Fatalf("restored poisoned authority revived = %v", err)
			}
			if fixture.authority.CheckOperator(bytes.Repeat([]byte{'M'}, localAPITokenBytes)) {
				t.Fatal("restored poisoned authority revived operator access")
			}
			fixture.authority.state.mu.Lock()
			fixture.authority.state.poisonErr = nil
			fixture.authority.state.mu.Unlock()
			fixture.close(t)
		})
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
		server, acceptErr := authority.Accept()
		if acceptErr == nil {
			acceptErr = authority.ReleaseConnection(server)
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
		probe func(context.Context, string) error
	}{
		{name: "permission denied", probe: func(context.Context, string) error { return unix.EACCES }},
		{name: "operation denied", probe: func(context.Context, string) error { return unix.EPERM }},
		{name: "timeout", probe: func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }},
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
			original := localAPIProbe
			localAPIProbe = test.probe
			_, openErr := home.OpenLocalAPI(context.Background())
			localAPIProbe = original
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
		original := localAPIProbe
		localAPIProbe = func(context.Context, string) error {
			if err := os.Rename(socketPath, socketPath+".first"); err != nil {
				t.Fatal(err)
			}
			makeStale()
			return unix.ECONNREFUSED
		}
		_, openErr := home.OpenLocalAPI(context.Background())
		localAPIProbe = original
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
	accepted := make(chan *net.UnixConn, 1)
	acceptErr := make(chan error, 1)
	go func() {
		connection, err := fixture.authority.Accept()
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
	var server *net.UnixConn
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
	if err := fixture.authority.ReleaseConnection(server); err != nil {
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

func TestLocalAPIAuthorityHomeCloseUnblocksBlockedAccept(t *testing.T) {
	fixture := newLocalAPIFixture(t, 'B')
	acceptDone := make(chan error, 1)
	go func() {
		connection, err := fixture.authority.Accept()
		if connection != nil {
			err = errors.Join(err, fixture.authority.ReleaseConnection(connection))
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

func TestLocalAPIAuthorityFinalIdentityCheckPreservesSocketReplacement(t *testing.T) {
	t.Run("owned close", func(t *testing.T) {
		fixture := newLocalAPIFixture(t, 'R')
		owned := fixture.socketPath + ".owned"
		var intruder *net.UnixListener
		original := localAPIBeforeUnlink
		localAPIBeforeUnlink = func(phase string) {
			if phase != "owned" {
				return
			}
			if err := os.Rename(fixture.socketPath, owned); err != nil {
				t.Error(err)
				return
			}
			var err error
			intruder, err = net.ListenUnix("unix", &net.UnixAddr{Name: fixture.socketPath, Net: "unix"})
			if err != nil {
				t.Error(err)
				return
			}
			intruder.SetUnlinkOnClose(false)
		}
		closeErr := fixture.home.Close()
		localAPIBeforeUnlink = original
		if !errors.Is(closeErr, ErrUncertain) {
			t.Fatalf("close after final-check replacement = %v", closeErr)
		}
		if intruder == nil {
			t.Fatal("intruder socket was not created")
		}
		for _, path := range []string{fixture.socketPath, owned} {
			if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
				t.Fatalf("socket %q was not preserved: %v, %v", filepath.Base(path), info, err)
			}
		}
		if err := intruder.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(fixture.socketPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(owned); err != nil {
			t.Fatal(err)
		}
		resetUncertainLocalAPIForCleanup(fixture.authority, fixture.home, true, true)
		fixture.close(t)
	})

	t.Run("stale activation", func(t *testing.T) {
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
		saved := socketPath + ".first"
		var replacement *net.UnixListener
		original := localAPIBeforeUnlink
		localAPIBeforeUnlink = func(phase string) {
			if phase != "stale" {
				return
			}
			if err := os.Rename(socketPath, saved); err != nil {
				t.Error(err)
				return
			}
			var err error
			replacement, err = net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
			if err != nil {
				t.Error(err)
				return
			}
			replacement.SetUnlinkOnClose(false)
		}
		authority, openErr := home.OpenLocalAPI(context.Background())
		localAPIBeforeUnlink = original
		if authority != nil || openErr == nil {
			t.Fatalf("stale final-check replacement = %v, %v", authority, openErr)
		}
		if replacement == nil {
			t.Fatal("stale replacement socket was not created")
		}
		for _, path := range []string{socketPath, saved} {
			if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
				t.Fatalf("stale socket %q was not preserved: %v, %v", filepath.Base(path), info, err)
			}
		}
		if err := replacement.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(socketPath); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(saved); err != nil {
			t.Fatal(err)
		}
		if err := home.Close(); err != nil {
			t.Fatal(err)
		}
	})
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
		original := localAPICloseListener
		localAPICloseListener = func(*net.UnixListener) error { return unix.EIO }
		first := fixture.home.Close()
		localAPICloseListener = original
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
				err := fixture.authority.Verify()
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
	if err := fixture.authority.Verify(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Verify after close = %v", err)
	}
}
