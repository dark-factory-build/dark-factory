package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"golang.org/x/sys/unix"
)

func TestParseExactWebCommands(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name    string
		args    []string
		command attemptCommand
		help    bool
	}{
		{name: "web help", args: []string{"web", "--help"}, help: true},
		{name: "status", args: []string{"web", "status"}, command: attemptCommand{kind: commandWebStatus}},
		{name: "pair", args: []string{"web", "pair"}, command: attemptCommand{kind: commandWebPair}},
		{name: "list", args: []string{"web", "list-clients"}, command: attemptCommand{kind: commandWebListClients}},
		{name: "list after", args: []string{"web", "list-clients", "--after", id}, command: attemptCommand{kind: commandWebListClients, after: id}},
		{name: "revoke", args: []string{"web", "revoke", id, "--revision", "7"}, command: attemptCommand{kind: commandWebRevoke, id: id, expectedRevision: 7}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, help, ok := parse(test.args)
			if !ok || help != test.help || !reflect.DeepEqual(command, test.command) {
				t.Fatalf("parse = %+v, help=%t, ok=%t", command, help, ok)
			}
		})
	}
}

func TestInvalidWebSyntaxStopsBeforeEnvironmentAndOpener(t *testing.T) {
	tests := [][]string{
		{"web", "pair", "extra"},
		{"web", "open"},
		{"web", "open", "--help"},
		{"web", "list-clients", "--after=0123456789abcdef0123456789abcdef"},
		{"web", "list-clients", "--after=0123456789abcdef0123456789abcdef", "--"},
		{"web", "list-clients", "-after", "0123456789abcdef0123456789abcdef"},
		{"web", "revoke", "0123456789abcdef0123456789abcdef", "--revision", "7", "--"},
		{"web", "list-clients", "--after", "00000000000000000000000000000000"},
		{"web", "revoke", "0123456789abcdef0123456789abcdef", "--revision", "0"},
		{"web", "revoke", "0123456789abcdef0123456789abcdef", "--revision", "01"},
		{"web", "revoke", "0123456789abcdef0123456789abcdef", "--revision", "7", "extra"},
	}
	for index, args := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			lookups, opens := 0, 0
			var stdout, stderr bytes.Buffer
			exit := runWithOpener(context.Background(), args, func(string) string {
				lookups++
				return "/private/should-not-be-read"
			}, &stdout, &stderr, func(context.Context, string) error {
				opens++
				return nil
			})
			if exit != exitUsage || lookups != 0 || opens != 0 || stdout.Len() != 0 || !usageRejection(stderr.String()) {
				t.Fatalf("run = exit %d lookups %d opens %d stdout %q stderr %q", exit, lookups, opens, stdout.String(), stderr.String())
			}
		})
	}
}

func TestWebStatusWithoutDaemonIsBoundedAndDoesNotReadOperatorToken(t *testing.T) {
	var stdout, stderr bytes.Buffer
	lookups := make([]string, 0, 2)
	exit := runWithOpener(context.Background(), []string{"web", "status"}, func(name string) string {
		lookups = append(lookups, name)
		if name == "DARK_FACTORY_SOCKET" {
			return "/private/missing-dark-factory.sock"
		}
		return "private-operator-token"
	}, &stdout, &stderr, nil)
	if exit != 0 || stderr.Len() != 0 || stdout.String() != `{"state":"stopped","ready":false,"address":"","path":"","origins":null,"active_clients":0,"revoked_clients":0,"active_challenges":0}`+"\n" {
		t.Fatalf("status = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
	if strings.Join(lookups, ",") != "DARK_FACTORY_SOCKET" {
		t.Fatalf("stopped status read configuration = %v", lookups)
	}
}

func TestWebStatusWithStaleSocketIsBoundedAndDoesNotReadOperatorToken(t *testing.T) {
	directory, err := os.MkdirTemp("/private/tmp", "df-stale-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	socket := filepath.Join(directory, "stale.sock")
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: socket}); err != nil {
		_ = unix.Close(fd)
		t.Fatal(err)
	}
	if err := unix.Close(fd); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(socket); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("stale socket = %v, %v", info, err)
	}
	var stdout, stderr bytes.Buffer
	lookups := make([]string, 0, 2)
	exit := runWithOpener(context.Background(), []string{"web", "status"}, func(name string) string {
		lookups = append(lookups, name)
		return "private-operator-token"
	}, &stdout, &stderr, nil)
	if exit != 0 || stderr.Len() != 0 || stdout.String() != `{"state":"stopped","ready":false,"address":"","path":"","origins":null,"active_clients":0,"revoked_clients":0,"active_challenges":0}`+"\n" {
		t.Fatalf("status = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
	if strings.Join(lookups, ",") != "DARK_FACTORY_SOCKET" {
		t.Fatalf("stopped status read configuration = %v", lookups)
	}
}

func TestWebListAndRevokeUseTypedOperatorCalls(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	id := "0123456789abcdef0123456789abcdef"
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		switch call.Kind() {
		case api.CallWebListClients:
			after, ok := call.WebListAfter()
			if !ok || after != id {
				return mustWebErrorReply(t, api.RemoteInvalidRequest)
			}
			reply, err := api.NewWebClientsReply(api.WebClientPage{Clients: []api.WebClient{{ID: id, CapabilityMask: 3, Revision: 1, CreatedAtMs: 1, UpdatedAtMs: 2}}})
			if err != nil {
				t.Fatal(err)
			}
			return reply
		case api.CallWebRevokeClient:
			input, ok := call.WebClientRevocationInput()
			if !ok || input.ID != id || input.ExpectedRevision != 9 {
				return mustWebErrorReply(t, api.RemoteInvalidRequest)
			}
			reply, err := api.NewWebRevokeReply(api.WebRevokeResult{ID: id, Revision: 2})
			if err != nil {
				t.Fatal(err)
			}
			return reply
		default:
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
	})
	var stdout, stderr bytes.Buffer
	if exit := runWithOpener(context.Background(), []string{"web", "list-clients", "--after", id}, webEnvironment(fixture), &stdout, &stderr, nil); exit != 0 {
		t.Fatalf("list exit = %d stderr = %q", exit, stderr.String())
	}
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	if !strings.Contains(stdout.String(), id) || strings.Contains(stdout.String(), "public_key") || strings.Contains(stdout.String(), "fingerprint") {
		t.Fatalf("list output = %q", stdout.String())
	}
	fixture.close(t)

	fixture = newAPIFixture(t)
	defer fixture.close(t)
	done = serveOne(fixture.listener, func(call api.Call) api.Reply {
		if call.Kind() != api.CallWebRevokeClient {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		reply, _ := api.NewWebRevokeReply(api.WebRevokeResult{ID: id, Revision: 2})
		return reply
	})
	stdout.Reset()
	stderr.Reset()
	if exit := runWithOpener(context.Background(), []string{"web", "revoke", id, "--revision", "9"}, webEnvironment(fixture), &stdout, &stderr, nil); exit != 0 {
		t.Fatalf("revoke exit = %d stderr = %q", exit, stderr.String())
	}
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	if !strings.Contains(stdout.String(), `"revision":2`) || stderr.Len() != 0 {
		t.Fatalf("revoke output = %q/%q", stdout.String(), stderr.String())
	}
}

// The pairing link is a credential: web pair hands it to the browser opener
// and nowhere else, including when the opener fails.
func TestWebPairHandsTheLinkOnlyToTheBrowser(t *testing.T) {
	const link = "https://app.darkfactory.build/#df_pair=00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	pair := func(call api.Call) api.Reply {
		if call.Kind() != api.CallWebPair {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		return api.NewContentReply(api.WebPair{Link: link})
	}
	done := serveMany(fixture.listener, pair, pair)
	for _, openErr := range []error{nil, errors.New("no desktop")} {
		var stdout, stderr bytes.Buffer
		opened := []string{}
		exit := runWithOpener(context.Background(), []string{"web", "pair"}, webEnvironment(fixture), &stdout, &stderr, func(_ context.Context, value string) error {
			opened = append(opened, value)
			return openErr
		})
		if want := map[bool]int{true: 0, false: exitFailure}[openErr == nil]; exit != want || len(opened) != 1 || opened[0] != link {
			t.Fatalf("web pair exit %d (want %d) opened %q stderr %q", exit, want, opened, stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), "df_pair") {
			t.Fatalf("web pair printed the link: %q / %q", stdout.String(), stderr.String())
		}
	}
	awaitMany(t, done, 2)
}

// A bare operator command finds the installed home; an attempt context never
// does, so a worker is never handed operator.token.
func TestOperatorEnvironmentDefaultsToTheInstalledHomeOutsideAttempts(t *testing.T) {
	environment := func(values map[string]string) func(string) string {
		return defaultOperatorHome(func(name string) string { return values[name] })
	}
	bare := environment(map[string]string{"HOME": "/Users/someone"})
	if got := bare("DARK_FACTORY_SOCKET"); got != "/Users/someone/.dark-factory/runtimes/factory.sock" {
		t.Fatalf("default socket = %q", got)
	}
	if got := bare("DARK_FACTORY_OPERATOR_TOKEN_FILE"); got != "/Users/someone/.dark-factory/operator.token" {
		t.Fatalf("default token = %q", got)
	}
	for _, values := range []map[string]string{
		{"HOME": "/Users/someone", "DARK_FACTORY_ATTEMPT_TOKEN_FILE": "/private/attempt.token"},
		{"HOME": "/Users/someone", "DARK_FACTORY_FACTORYCTL": "/usr/local/bin/factoryctl"},
		{"HOME": "/Users/someone", "DARK_FACTORY_SOCKET": "/private/other.sock"},
	} {
		if got := environment(values)("DARK_FACTORY_OPERATOR_TOKEN_FILE"); got != "" {
			t.Fatalf("%v fell back to %q", values, got)
		}
	}
}

func TestWebRevokeReportsCommittedCleanupUncertainty(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	id := "0123456789abcdef0123456789abcdef"
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		if call.Kind() != api.CallWebRevokeClient {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		return mustWebErrorReply(t, api.RemoteCleanupUnresolved)
	})
	var stdout, stderr bytes.Buffer
	exit := runWithOpener(context.Background(), []string{"web", "revoke", id, "--revision", "9"}, webEnvironment(fixture), &stdout, &stderr, nil)
	result := awaitServer(t, done)
	if exit == 0 || result.err != nil || stdout.Len() != 0 || stderr.String() != "factoryctl: web revoke: local API completed revocation but could not prove browser cleanup\n" {
		t.Fatalf("cleanup uncertainty = exit %d stdout %q stderr %q server %v", exit, stdout.String(), stderr.String(), result.err)
	}
}

// TestWebRevokeAcknowledgedAfterPostCommitCleanup replays the exact reported
// sequence: the local API commits the revocation, spends the rest of its
// dispatch budget tearing down the sockets that revocation revokes, and only
// then writes the reply carrying the committed revision. factoryctl must print
// that revision rather than a timeout its own next command contradicts.
func TestWebRevokeAcknowledgedAfterPostCommitCleanup(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	id := "0123456789abcdef0123456789abcdef"
	// Longer than the five seconds factoryctl once allowed a whole call, and
	// inside the ten-second budget the daemon gives an ordinary dispatch.
	const cleanup = 6 * time.Second
	done := serveOneAfter(fixture.listener, cleanup, func(call api.Call) api.Reply {
		input, ok := call.WebClientRevocationInput()
		if !ok || input.ID != id || input.ExpectedRevision != 9 {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		reply, err := api.NewWebRevokeReply(api.WebRevokeResult{ID: id, Revision: 10})
		if err != nil {
			return mustWebErrorReply(t, api.RemoteInternal)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := runWithOpener(context.Background(), []string{"web", "revoke", id, "--revision", "9"}, webEnvironment(fixture), &stdout, &stderr, nil)
	result := awaitServer(t, done)
	if exit != 0 || result.err != nil || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"revision":10`) {
		t.Fatalf("late acknowledgement = exit %d stdout %q stderr %q server %v", exit, stdout.String(), stderr.String(), result.err)
	}
}

// serveOneAfter answers exactly one call the way the daemon does when work
// after the durable commit precedes the reply: receive, spend delay, then
// refresh the dispatch deadline and respond.
func serveOneAfter(listener *api.Listener, delay time.Duration, reply func(api.Call) api.Reply) <-chan serverResult {
	done := make(chan serverResult, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- serverResult{err: err}
			return
		}
		defer connection.Close()
		ctx, cancel := context.WithTimeout(context.Background(), delay+5*time.Second)
		defer cancel()
		call, err := connection.Receive(ctx)
		if err == nil {
			time.Sleep(delay)
			if err = connection.RefreshDeadline(ctx); err == nil {
				var response api.Reply
				if response, err = connection.Dispatch(reply); err == nil {
					err = connection.Respond(response)
				}
			}
		}
		done <- serverResult{call: call, err: err}
	}()
	return done
}

func webEnvironment(fixture *apiFixture) func(string) string {
	return func(name string) string {
		switch name {
		case "DARK_FACTORY_SOCKET":
			return fixture.socket
		case "DARK_FACTORY_OPERATOR_TOKEN_FILE":
			return fixture.directory + "/home/operator.token"
		default:
			return ""
		}
	}
}

func serveMany(listener *api.Listener, replies ...func(api.Call) api.Reply) <-chan []serverResult {
	done := make(chan []serverResult, 1)
	go func() {
		results := make([]serverResult, 0, len(replies))
		for _, reply := range replies {
			connection, err := listener.Accept()
			if err != nil {
				results = append(results, serverResult{err: err})
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			call, receiveErr := connection.Receive(ctx)
			cancel()
			if receiveErr == nil {
				var response api.Reply
				response, receiveErr = connection.Dispatch(reply)
				if receiveErr == nil {
					receiveErr = connection.Respond(response)
				}
			}
			_ = connection.Close()
			results = append(results, serverResult{call: call, err: receiveErr})
		}
		done <- results
	}()
	return done
}

func awaitMany(t testing.TB, done <-chan []serverResult, want int) []serverResult {
	t.Helper()
	select {
	case results := <-done:
		if len(results) != want {
			t.Fatalf("server calls = %d, want %d", len(results), want)
		}
		return results
	case <-time.After(2 * time.Second):
		t.Fatal("server calls did not finish")
		return nil
	}
}

func mustWebErrorReply(t *testing.T, code api.RemoteErrorCode) api.Reply {
	t.Helper()
	reply, err := api.NewErrorReply(code)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}
