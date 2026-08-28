//go:build darwin

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type recordedLaunchctl struct {
	results []launchctlResult
	calls   [][]string
}

func (fake *recordedLaunchctl) run(_ context.Context, arguments ...string) launchctlResult {
	fake.calls = append(fake.calls, append([]string(nil), arguments...))
	if len(fake.results) == 0 {
		return launchctlResult{status: -1, err: errors.New("unexpected launchctl call")}
	}
	result := fake.results[0]
	fake.results = fake.results[1:]
	return result
}

func TestServiceStatusExactAbsenceIsReadOnly(t *testing.T) {
	root := serviceTestRoot(t)
	home := filepath.Join(root, "factory")
	userHome := filepath.Join(root, "user")
	if err := os.Mkdir(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	before := snapshotServiceTrees(t, home, userHome)
	fake := &recordedLaunchctl{results: []launchctlResult{
		{status: launchctlNotFound},
		{status: 0, stdout: []byte(launchctlNotFoundText + "\n")},
	}}
	status, err := inspectService(context.Background(), home, userHome, fake.run)
	if err != nil || status != (ServiceStatus{State: ServiceAbsent}) {
		t.Fatalf("status = %+v, %v", status, err)
	}
	wantService := fmt.Sprintf("gui/%d/%s", os.Geteuid(), serviceLabel)
	wantCalls := [][]string{{"print", wantService}, {"error", "113"}}
	if !reflect.DeepEqual(fake.calls, wantCalls) {
		t.Fatalf("launchctl calls = %q, want %q", fake.calls, wantCalls)
	}
	after := snapshotServiceTrees(t, home, userHome)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only status changed filesystem\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestServiceStatusRefusesNonGoHomesBeforeLaunchctl(t *testing.T) {
	root := serviceTestRoot(t)
	userHome := filepath.Join(root, "user")
	if err := os.Mkdir(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		build func(string) error
	}{
		{name: "rust", build: func(path string) error {
			if err := os.Mkdir(path, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(path, "factory.db"), []byte("rust"), 0o600)
		}},
		{name: "unknown", build: func(path string) error {
			if err := os.Mkdir(path, 0o700); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(path, "format"), []byte("unknown\n"), 0o600)
		}},
		{name: "symlink", build: func(path string) error {
			target := path + "-target"
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			return os.Symlink(target, path)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := filepath.Join(root, "home-"+test.name)
			if err := test.build(home); err != nil {
				t.Fatal(err)
			}
			before := snapshotServiceTrees(t, root)
			calls := 0
			_, err := inspectService(context.Background(), home, userHome, func(context.Context, ...string) launchctlResult {
				calls++
				return launchctlResult{}
			})
			if err == nil || calls != 0 {
				t.Fatalf("status error = %v, launchctl calls = %d", err, calls)
			}
			if after := snapshotServiceTrees(t, root); !reflect.DeepEqual(before, after) {
				t.Fatal("refused home was modified")
			}
		})
	}
}

func TestLaunchctlAbsenceClassificationIsExact(t *testing.T) {
	service := fmt.Sprintf("gui/%d/%s", os.Geteuid(), serviceLabel)
	plist := "/private/tmp/user/Library/LaunchAgents/" + servicePlistName
	program := "/private/tmp/factory/bin/current/factoryd"
	for _, test := range []struct {
		name    string
		results []launchctlResult
		want    bool
	}{
		{name: "documented", results: []launchctlResult{{status: 113}, {status: 0, stdout: []byte(launchctlNotFoundText + "\n")}}, want: true},
		{name: "permission", results: []launchctlResult{{status: 1, stderr: []byte("Operation not permitted\n")}}},
		{name: "wrong status same text", results: []launchctlResult{{status: 3}, {status: 0, stdout: []byte(launchctlNotFoundText + "\n")}}},
		{name: "wrong text", results: []launchctlResult{{status: 113}, {status: 0, stdout: []byte("113: other\n")}}},
		{name: "classification stderr", results: []launchctlResult{{status: 113}, {status: 0, stdout: []byte(launchctlNotFoundText + "\n"), stderr: []byte("warning")}}},
		{name: "classification failed", results: []launchctlResult{{status: 113}, {status: 1}}},
		{name: "spawn", results: []launchctlResult{{status: -1, err: errors.New("spawn")}}},
		{name: "oversized", results: []launchctlResult{{status: 0, overflow: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &recordedLaunchctl{results: append([]launchctlResult(nil), test.results...)}
			observation, err := observeLaunchctl(context.Background(), fake.run, service, plist, program)
			if test.want {
				if err != nil || observation.present {
					t.Fatalf("absence = %+v, %v", observation, err)
				}
			} else if !errors.Is(err, ErrServiceLaunchctl) {
				t.Fatalf("classification error = %v", err)
			}
		})
	}
}

func TestLaunchctlCommandCancellationReapsAndBoundsTheExactChild(t *testing.T) {
	root := serviceTestRoot(t)
	pidPath := filepath.Join(root, "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := runLaunchctlBinary(ctx, os.Args[0], "-test.run=^TestLaunchctlCommandHelper$", "--", "hang", pidPath)
	if result.status != -1 || !errors.Is(result.err, context.DeadlineExceeded) {
		t.Fatalf("hung launchctl = status %d, err %v, stderr %q", result.status, result.err, result.stderr)
	}
	body, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil || pid <= 1 {
		t.Fatalf("helper pid = %q, %v", body, err)
	}
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("launchctl child %d was not reaped: %v", pid, err)
	}

	result = runLaunchctlBinary(context.Background(), os.Args[0], "-test.run=^TestLaunchctlCommandHelper$", "--", "overflow")
	if result.err != nil || result.status != 0 || !result.overflow || len(result.stdout) != launchctlOutputLimit {
		t.Fatalf("oversized launchctl = status %d err %v overflow %t size %d", result.status, result.err, result.overflow, len(result.stdout))
	}
	result = runLaunchctlBinary(context.Background(), filepath.Join(root, "missing"), "print", "service")
	if result.status != -1 || result.err == nil {
		t.Fatalf("missing launchctl = status %d err %v", result.status, result.err)
	}
}

func TestLaunchctlCommandHelper(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	switch os.Args[separator+1] {
	case "hang":
		if separator+2 >= len(os.Args) {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Args[separator+2], []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "overflow":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte{'x'}, launchctlOutputLimit+1))
		os.Exit(0)
	default:
		os.Exit(4)
	}
}

func TestLaunchctlPrintRequiresExactOwnedFields(t *testing.T) {
	service := fmt.Sprintf("gui/%d/%s", os.Geteuid(), serviceLabel)
	plist := "/private/tmp/user/Library/LaunchAgents/" + servicePlistName
	program := "/private/tmp/factory/bin/current/factoryd"
	valid := func(state, pid string) []byte {
		pidLine := ""
		if pid != "" {
			pidLine = "\n\tpid = " + pid
		}
		return []byte(service + " = {\n\tpath = " + plist + "\n\tstate = " + state + "\n\tprogram = " + program + pidLine + "\n}\n")
	}
	if pid, err := parseLaunchctlPrint(valid("running", "731"), service, plist, program); err != nil || pid != 731 {
		t.Fatalf("running parse = %d, %v", pid, err)
	}
	if pid, err := parseLaunchctlPrint(valid("not running", ""), service, plist, program); err != nil || pid != 0 {
		t.Fatalf("stopped parse = %d, %v", pid, err)
	}
	fake := &recordedLaunchctl{results: []launchctlResult{{status: 0, stdout: valid("running", "731"), stderr: []byte("warning")}}}
	if _, err := observeLaunchctl(context.Background(), fake.run, service, plist, program); !errors.Is(err, ErrServiceLaunchctl) {
		t.Fatalf("successful print stderr accepted: %v", err)
	}
	mutations := map[string][]byte{
		"foreign label":   bytes.Replace(valid("running", "731"), []byte(service), []byte("gui/501/com.foreign"), 1),
		"wildcard label":  bytes.Replace(valid("running", "731"), []byte(service), []byte("gui/501/*"), 1),
		"foreign plist":   bytes.Replace(valid("running", "731"), []byte(plist), []byte("/private/foreign.plist"), 1),
		"foreign program": bytes.Replace(valid("running", "731"), []byte(program), []byte("/private/foreign"), 1),
		"missing pid":     valid("running", ""),
		"pid one":         valid("running", "1"),
		"unknown state":   valid("waiting", ""),
		"stopped pid":     valid("not running", "731"),
		"duplicate pid":   bytes.Replace(valid("running", "731"), []byte("\n}"), []byte("\n\tpid = 732\n}"), 1),
		"nul":             append(valid("running", "731"), 0),
	}
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := parseLaunchctlPrint(mutation, service, plist, program); !errors.Is(err, ErrServiceLaunchctl) {
				t.Fatalf("mutation accepted: %v", err)
			}
		})
	}
}

func TestServicePlistIsOneFiniteAllowlist(t *testing.T) {
	home := "/private/tmp/factory & <operator>"
	body, digest, err := ServicePlist(home)
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(body) != digest || bytes.Count(body, []byte("<key>AbandonProcessGroup</key>")) != 1 || bytes.Count(body, []byte("<true/>")) != 2 {
		t.Fatalf("plist identity or required keys invalid: %s", body)
	}
	for _, forbidden := range []string{"KeepAlive", "EnvironmentVariables", "Sockets", "NetworkState", "StandardOutPath", "StandardErrorPath", "*"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("plist contains forbidden %q", forbidden)
		}
	}
	for _, expected := range []string{"factory &amp; &lt;operator&gt;", serviceLabel, "--home", "<integer>63</integer>"} {
		if !bytes.Contains(body, []byte(expected)) {
			t.Fatalf("plist omitted %q", expected)
		}
	}
	if _, _, err := ServicePlist("relative"); !errors.Is(err, ErrServicePlist) {
		t.Fatalf("relative home = %v", err)
	}
}

func TestServiceStatusRefusesPlistMutationsAndPresentJobs(t *testing.T) {
	root := serviceTestRoot(t)
	home := filepath.Join(root, "factory")
	userHome := filepath.Join(root, "user")
	launchAgents := filepath.Join(userHome, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgents, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	plistBody, _, err := ServicePlist(home)
	if err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(launchAgents, servicePlistName)
	if err := os.WriteFile(plistPath, plistBody, 0o600); err != nil {
		t.Fatal(err)
	}
	absent := &recordedLaunchctl{results: []launchctlResult{{status: 113}, {status: 0, stdout: []byte(launchctlNotFoundText + "\n")}}}
	if status, err := inspectService(context.Background(), home, userHome, absent.run); status.State != ServiceAmbiguous || !errors.Is(err, ErrServiceAmbiguous) {
		t.Fatalf("plist without receipt = %+v, %v", status, err)
	}

	if err := os.WriteFile(plistPath, append(plistBody, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if status, err := inspectService(context.Background(), home, userHome, func(context.Context, ...string) launchctlResult {
		calls++
		return launchctlResult{}
	}); status.State != ServiceAmbiguous || !errors.Is(err, ErrServicePlist) || calls != 0 {
		t.Fatalf("tampered plist = %+v, %v, calls=%d", status, err, calls)
	}

	if err := os.Remove(plistPath); err != nil {
		t.Fatal(err)
	}
	service := fmt.Sprintf("gui/%d/%s", os.Geteuid(), serviceLabel)
	program := filepath.Join(home, "bin", "current", "factoryd")
	presentOutput := []byte(service + " = {\n\tpath = " + plistPath + "\n\tstate = running\n\tprogram = " + program + "\n\tpid = 731\n}\n")
	present := &recordedLaunchctl{results: []launchctlResult{{status: 0, stdout: presentOutput}}}
	if status, err := inspectService(context.Background(), home, userHome, present.run); status != (ServiceStatus{State: ServiceAmbiguous, PID: 731}) || !errors.Is(err, ErrServiceAmbiguous) {
		t.Fatalf("present job without receipt = %+v, %v", status, err)
	}
}

func TestServiceStatusRejectsPlistMetadataWithoutLaunchctl(t *testing.T) {
	root := serviceTestRoot(t)
	home := filepath.Join(root, "factory")
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	body, _, err := ServicePlist(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{name: "wrong mode", mutate: func(t *testing.T, path, _ string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, path, directory string) {
			if err := os.Link(path, filepath.Join(directory, "second-link")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, path, directory string) {
			target := filepath.Join(directory, "target")
			if err := os.Rename(path, target); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			userHome := filepath.Join(root, "user-"+strings.ReplaceAll(test.name, " ", "-"))
			directory := filepath.Join(userHome, "Library", "LaunchAgents")
			if err := os.MkdirAll(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, servicePlistName)
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, path, directory)
			calls := 0
			status, err := inspectService(context.Background(), home, userHome, func(context.Context, ...string) launchctlResult {
				calls++
				return launchctlResult{}
			})
			if status.State != ServiceAmbiguous || !errors.Is(err, ErrServicePlist) || calls != 0 {
				t.Fatalf("metadata mutation = %+v, %v, calls=%d", status, err, calls)
			}
		})
	}
}

func TestServiceStatusCancellationAndRepeatedCallsLeakNothing(t *testing.T) {
	root := serviceTestRoot(t)
	home := filepath.Join(root, "factory")
	userHome := filepath.Join(root, "user")
	if err := os.Mkdir(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := inspectService(ctx, home, userHome, func(ctx context.Context, _ ...string) launchctlResult {
		cancel()
		return launchctlResult{status: -1, err: ctx.Err()}
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrServiceLaunchctl) {
		t.Fatalf("cancel error = %v", err)
	}
	baselineFD := serviceFDCount(t)
	baselineGoroutines := runtime.NumGoroutine()
	for index := 0; index < 20; index++ {
		fake := &recordedLaunchctl{results: []launchctlResult{{status: 113}, {status: 0, stdout: []byte(launchctlNotFoundText + "\n")}}}
		if _, err := inspectService(context.Background(), home, userHome, fake.run); err != nil {
			t.Fatal(err)
		}
	}
	if got := serviceFDCount(t); got != baselineFD {
		t.Fatalf("FD count = %d, want %d", got, baselineFD)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() != baselineGoroutines && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	if got := runtime.NumGoroutine(); got != baselineGoroutines {
		t.Fatalf("goroutines = %d, want %d", got, baselineGoroutines)
	}
}

type serviceTreeEntry struct {
	Mode    os.FileMode
	Size    int64
	ModTime int64
	Digest  [sha256.Size]byte
	Link    string
}

func snapshotServiceTrees(t *testing.T, roots ...string) map[string]serviceTreeEntry {
	t.Helper()
	result := make(map[string]serviceTreeEntry)
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			entry := serviceTreeEntry{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime().UnixNano()}
			if info.Mode().IsRegular() {
				body, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				entry.Digest = sha256.Sum256(body)
			} else if info.Mode()&os.ModeSymlink != 0 {
				link, readErr := os.Readlink(path)
				if readErr != nil {
					return readErr
				}
				entry.Link = link
			}
			result[path] = entry
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func serviceTestRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/private/tmp", "dark-factory-service-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func serviceFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
