package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"github.com/dark-factory-build/dark-factory/internal/daemon"
	"github.com/dark-factory-build/dark-factory/internal/install"
)

const (
	defaultBrowserAddress = "127.0.0.1:43123"
	defaultBrowserOrigin  = "https://app.darkfactory.build"
	maxHomeArgumentBytes  = 4096
	maxAPIHandlers        = 32
	apiHandlerTimeout     = 10 * time.Second
	exitFailure           = 1
	exitUsage             = 64

	usage = `usage:
  factoryd --home ABSOLUTE [--development-browser-origin EXACT_ORIGIN ...]
  factoryd --version
  factoryd --build-identity
`
)

var (
	cleanStartupCancellation = errors.New("factoryd: clean startup cancellation")
	startupPhaseHook         func(string)
	closeDaemon              = func(value *daemon.Daemon) error { return value.Close() }
	closeRuntimeParent       = func(value *daemon.RuntimeParent) error { return value.Close() }
)

type config struct {
	home           string
	browserAddress string
	browserOrigins []string
}

type process struct {
	lifecycleMu sync.Mutex
	closeDone   chan struct{}
	closeErr    error
	waitDone    chan struct{}
	waitErr     error

	cancel context.CancelFunc

	home          *install.OperationalHome
	runtimeParent *daemon.RuntimeParent
	daemon        *daemon.Daemon
	apiAuthority  *install.LocalAPIAuthority
	listener      *api.Listener
	browser       *daemon.BrowserRuntime

	apiDone  chan struct{}
	apiErr   error
	apiStart bool
	handlers sync.WaitGroup
	slots    chan struct{}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintf(stdout, "factoryd %s\n", buildinfo.Current().Version())
		return 0
	}
	if len(args) == 1 && args[0] == "--build-identity" {
		if err := buildinfo.Current().WriteJSON(stdout); err != nil {
			return exitFailure
		}
		return 0
	}
	configuration, help, ok := parse(args)
	if help {
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	if !ok {
		_, _ = io.WriteString(stderr, usage)
		return exitUsage
	}
	if err := serve(ctx, configuration); err != nil {
		_, _ = io.WriteString(stderr, "factoryd: runtime unavailable\n")
		return exitFailure
	}
	return 0
}

func parse(args []string) (config, bool, bool) {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return config{}, true, true
	}
	result := config{browserAddress: defaultBrowserAddress, browserOrigins: []string{defaultBrowserOrigin}}
	for index := 0; index < len(args); {
		if index+1 >= len(args) {
			return config{}, false, false
		}
		name, value := args[index], args[index+1]
		switch name {
		case "--home":
			if result.home != "" || !validHome(value) {
				return config{}, false, false
			}
			result.home = value
		case "--development-browser-origin":
			if !validBrowserOrigin(value) || len(result.browserOrigins) == 8 {
				return config{}, false, false
			}
			for _, present := range result.browserOrigins {
				if present == value {
					return config{}, false, false
				}
			}
			result.browserOrigins = append(result.browserOrigins, value)
		default:
			return config{}, false, false
		}
		index += 2
	}
	if result.home == "" {
		return config{}, false, false
	}
	return result, false, true
}

func validHome(value string) bool {
	return value != "/" && len(value) > 1 && len(value) <= maxHomeArgumentBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func validBrowserAddress(value string, allowZero bool) bool {
	host, rawPort, err := net.SplitHostPort(value)
	if err != nil || host != "127.0.0.1" || rawPort == "" {
		return false
	}
	port, err := strconv.Atoi(rawPort)
	minimum := 1
	if allowZero {
		minimum = 0
	}
	return err == nil && port >= minimum && port <= 65535 && strconv.Itoa(port) == rawPort
}

func validBrowserOrigin(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && parsed.String() == value && !strings.Contains(value, "*") && len(value) <= 8192
}

func serve(ctx context.Context, configuration config) error {
	if ctx == nil || !validHome(configuration.home) || !validBrowserAddress(configuration.browserAddress, true) || len(configuration.browserOrigins) == 0 {
		return errors.New("invalid factoryd configuration")
	}
	for _, origin := range configuration.browserOrigins {
		if !validBrowserOrigin(origin) {
			return errors.New("invalid factoryd configuration")
		}
	}
	owner, err := openProcess(ctx, configuration)
	if err != nil {
		if errors.Is(err, cleanStartupCancellation) {
			return nil
		}
		return err
	}
	return owner.wait(ctx)
}

func openProcess(ctx context.Context, configuration config) (_ *process, resultErr error) {
	ownedContext, cancel := context.WithCancel(ctx)
	owner := &process{cancel: cancel, apiDone: make(chan struct{}), slots: make(chan struct{}, maxAPIHandlers)}
	keep := false
	defer func() {
		if !keep {
			cleanupErr := owner.close()
			resultErr = errors.Join(resultErr, cleanupErr)
			if cleanupErr == nil && errors.Is(resultErr, context.Canceled) {
				resultErr = errors.Join(resultErr, cleanStartupCancellation)
			}
		}
	}()

	var err error
	owner.home, err = install.OpenOperationalHome(ownedContext, configuration.home)
	if err != nil {
		return nil, err
	}
	startupPhase("home")
	store, err := owner.home.OpenStore(ownedContext)
	if err != nil {
		return nil, err
	}
	startupPhase("store")
	runtimes, err := owner.home.Runtimes()
	if err != nil {
		return nil, err
	}
	owner.runtimeParent, err = daemon.OpenRuntimeParent(ownedContext, runtimes, filepath.Join(configuration.home, "runtimes"))
	if err != nil {
		return nil, err
	}
	startupPhase("runtime parent")
	owner.daemon, err = daemon.NewDaemon(store)
	if err != nil {
		return nil, err
	}
	startupPhase("daemon")
	owner.apiAuthority, err = owner.home.OpenLocalAPI(ownedContext)
	if err != nil {
		return nil, err
	}
	startupPhase("local API")
	owner.listener, err = api.Listen(owner.apiAuthority)
	if err != nil {
		return nil, err
	}
	startupPhase("listener")
	owner.browser, err = owner.daemon.ListenBrowser(configuration.browserAddress, configuration.browserOrigins)
	if err != nil {
		return nil, err
	}
	startupPhase("browser")
	owner.apiStart = true
	go owner.accept(ownedContext, owner.listener)
	keep = true
	return owner, nil
}

func (owner *process) accept(ctx context.Context, listener *api.Listener) {
	defer close(owner.apiDone)
	for {
		select {
		case owner.slots <- struct{}{}:
		case <-ctx.Done():
			owner.apiErr = ctx.Err()
			return
		}
		connection, err := listener.Accept()
		if err != nil {
			<-owner.slots
			owner.apiErr = err
			return
		}
		owner.handlers.Add(1)
		go func() {
			defer owner.handlers.Done()
			defer func() { <-owner.slots }()
			handlerContext, cancel := context.WithTimeout(ctx, apiHandlerTimeout)
			defer cancel()
			_ = owner.daemon.HandleConnection(handlerContext, connection)
		}()
	}
}

func (owner *process) wait(ctx context.Context) error {
	if owner == nil || ctx == nil {
		return errors.New("invalid factoryd owner")
	}
	owner.lifecycleMu.Lock()
	if owner.waitDone != nil {
		done := owner.waitDone
		owner.lifecycleMu.Unlock()
		<-done
		owner.lifecycleMu.Lock()
		err := owner.waitErr
		owner.lifecycleMu.Unlock()
		return err
	}
	if owner.closeDone != nil {
		done := owner.closeDone
		owner.lifecycleMu.Unlock()
		<-done
		owner.lifecycleMu.Lock()
		err := owner.closeErr
		owner.lifecycleMu.Unlock()
		return err
	}
	if owner.browser == nil || owner.apiDone == nil {
		owner.lifecycleMu.Unlock()
		return errors.New("invalid factoryd owner")
	}
	waitDone := make(chan struct{})
	owner.waitDone = waitDone
	apiDone, browser := owner.apiDone, owner.browser
	owner.lifecycleMu.Unlock()
	var cause error
	select {
	case <-ctx.Done():
	case <-apiDone:
		owner.lifecycleMu.Lock()
		closing := owner.closeDone != nil
		owner.lifecycleMu.Unlock()
		if ctx.Err() == nil && !closing && !errors.Is(owner.apiErr, context.Canceled) {
			cause = fmt.Errorf("local API owner stopped: %w", owner.apiErr)
		}
	case <-browser.Done():
		owner.lifecycleMu.Lock()
		closing := owner.closeDone != nil
		owner.lifecycleMu.Unlock()
		if ctx.Err() == nil && !closing {
			if err := browser.Err(); err != nil {
				cause = err
			} else {
				cause = errors.New("browser owner stopped")
			}
		}
	}
	result := errors.Join(cause, owner.close())
	owner.lifecycleMu.Lock()
	owner.waitErr = result
	close(owner.waitDone)
	owner.lifecycleMu.Unlock()
	return result
}

func (owner *process) close() error {
	if owner == nil {
		return nil
	}
	owner.lifecycleMu.Lock()
	if owner.closeDone != nil {
		done := owner.closeDone
		owner.lifecycleMu.Unlock()
		<-done
		owner.lifecycleMu.Lock()
		err := owner.closeErr
		owner.lifecycleMu.Unlock()
		return err
	}
	owner.closeDone = make(chan struct{})
	owner.lifecycleMu.Unlock()
	result := owner.shutdown()
	owner.lifecycleMu.Lock()
	owner.closeErr = result
	close(owner.closeDone)
	owner.lifecycleMu.Unlock()
	return result
}

func (owner *process) shutdown() error {
	if owner.cancel != nil {
		owner.cancel()
	}
	var result error
	listener := owner.listener
	if owner.listener != nil {
		result = errors.Join(result, listener.Close())
	} else if owner.apiAuthority != nil {
		result = errors.Join(result, owner.apiAuthority.Close())
	}
	if result == nil {
		owner.lifecycleMu.Lock()
		owner.listener = nil
		owner.apiAuthority = nil
		owner.lifecycleMu.Unlock()
	}
	if owner.apiStart {
		<-owner.apiDone
	}
	owner.handlers.Wait()
	if owner.daemon != nil {
		if err := closeDaemon(owner.daemon); err != nil {
			result = errors.Join(result, err)
		} else {
			owner.lifecycleMu.Lock()
			owner.daemon = nil
			owner.browser = nil
			owner.lifecycleMu.Unlock()
		}
	}
	if result != nil {
		return result
	}
	if owner.runtimeParent != nil {
		if err := closeRuntimeParent(owner.runtimeParent); err != nil {
			result = errors.Join(result, err)
		} else {
			owner.lifecycleMu.Lock()
			owner.runtimeParent = nil
			owner.lifecycleMu.Unlock()
		}
	}
	if result != nil {
		return result
	}
	if owner.home != nil {
		result = errors.Join(result, owner.home.Close())
		if result == nil {
			owner.lifecycleMu.Lock()
			owner.home = nil
			owner.lifecycleMu.Unlock()
		}
	}
	return result
}

func startupPhase(name string) {
	if startupPhaseHook != nil {
		startupPhaseHook(name)
	}
}
