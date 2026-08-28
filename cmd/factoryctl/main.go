package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/install"
)

const (
	attemptRequestTimeout = 5 * time.Second
	exitUsage             = 64
	exitFailure           = 1

	usage = `usage:
  factoryctl attempt succeed [--result TEXT]
  factoryctl attempt block --detail TEXT
  factoryctl attempt fail [--detail TEXT]
  factoryctl attempt request-human --idempotency-key HEX32 --question TEXT
  factoryctl web status
  factoryctl web open
  factoryctl web list-clients [--after CLIENT_ID]
  factoryctl web revoke CLIENT_ID --revision REVISION
  factoryctl init --home ABSOLUTE
  factoryctl doctor --home ABSOLUTE
`
)

type commandKind uint8

const (
	commandSucceed commandKind = iota + 1
	commandBlock
	commandFail
	commandRequestHuman
	commandWebStatus
	commandWebOpen
	commandWebListClients
	commandWebRevoke
	commandInit
	commandDoctor
)

type attemptCommand struct {
	kind             commandKind
	home             string
	idempotencyKey   string
	text             string
	id               string
	after            string
	expectedRevision uint64
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	return runWithOpener(ctx, args, getenv, stdout, stderr, openBrowser)
}

type browserOpener func(context.Context, string) error

func runWithOpener(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, opener browserOpener) int {
	command, help, ok := parse(args)
	if help {
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	if !ok {
		_, _ = io.WriteString(stderr, usage)
		return exitUsage
	}
	if command.kind == commandInit || command.kind == commandDoctor {
		return runHome(ctx, command, stdout, stderr)
	}
	if command.kind >= commandWebStatus {
		return runWeb(ctx, command, getenv, stdout, stderr, opener)
	}

	socket := getenv("DARK_FACTORY_SOCKET")
	if socket == "" {
		_, _ = io.WriteString(stderr, "factoryctl: attempt client configuration is invalid\n")
		return exitFailure
	}
	client, err := api.NewAttemptClientFromEnvironment(socket)
	if err != nil {
		writeFailure(stderr, command.kind, err)
		return exitFailure
	}

	callContext, cancel := context.WithTimeout(ctx, attemptRequestTimeout)
	defer cancel()
	var result api.MutationResult
	switch command.kind {
	case commandSucceed:
		result, err = client.Succeed(callContext, command.text)
	case commandBlock:
		result, err = client.Block(callContext, command.text)
	case commandFail:
		result, err = client.Fail(callContext, command.text)
	case commandRequestHuman:
		result, err = client.RequestHuman(callContext, api.HumanQuestionInput{IdempotencyKey: command.idempotencyKey, Question: command.text})
	default:
		err = api.ErrInvalidInput
	}
	if err != nil {
		writeFailure(stderr, command.kind, err)
		return exitFailure
	}
	if command.kind == commandRequestHuman {
		_, _ = fmt.Fprintf(stdout, "human request accepted: head=%d revision=%d\n", result.Head, result.Revision)
	} else {
		_, _ = fmt.Fprintf(stdout, "attempt outcome request accepted: head=%d revision=%d\n", result.Head, result.Revision)
	}
	return 0
}

func parse(args []string) (attemptCommand, bool, bool) {
	if len(args) == 1 && helpFlag(args[0]) {
		return attemptCommand{}, true, true
	}
	if len(args) == 3 && (args[0] == "init" || args[0] == "doctor") && args[1] == "--home" {
		if validHomeArg(args[2]) {
			kind := commandInit
			if args[0] == "doctor" {
				kind = commandDoctor
			}
			return attemptCommand{kind: kind, home: args[2]}, false, true
		}
	}
	if len(args) == 2 && (args[0] == "init" || args[0] == "doctor") && helpFlag(args[1]) {
		return attemptCommand{}, true, true
	}
	if len(args) < 2 || args[0] != "attempt" && args[0] != "web" {
		return attemptCommand{}, false, false
	}
	if len(args) == 2 && helpFlag(args[1]) {
		return attemptCommand{}, true, true
	}
	if len(args) == 3 && helpFlag(args[2]) {
		switch args[1] {
		case "succeed", "block", "fail", "request-human":
			return attemptCommand{}, true, true
		case "status", "open", "list-clients", "revoke":
			if args[0] == "web" {
				return attemptCommand{}, true, true
			}
		default:
			return attemptCommand{}, false, false
		}
	}

	if args[0] == "web" {
		return parseWeb(args)
	}
	switch args[1] {
	case "succeed":
		if len(args) == 2 {
			return attemptCommand{kind: commandSucceed}, false, true
		}
		if len(args) == 4 && args[2] == "--result" {
			return attemptCommand{kind: commandSucceed, text: args[3]}, false, true
		}
	case "block":
		if len(args) == 4 && args[2] == "--detail" && args[3] != "" {
			return attemptCommand{kind: commandBlock, text: args[3]}, false, true
		}
	case "fail":
		if len(args) == 2 {
			return attemptCommand{kind: commandFail}, false, true
		}
		if len(args) == 4 && args[2] == "--detail" {
			return attemptCommand{kind: commandFail, text: args[3]}, false, true
		}
	case "request-human":
		if len(args) == 6 && args[2] == "--idempotency-key" && validHumanRequestKey(args[3]) && args[4] == "--question" && validQuestion(args[5]) {
			return attemptCommand{kind: commandRequestHuman, idempotencyKey: args[3], text: args[5]}, false, true
		}
	}
	return attemptCommand{}, false, false
}

func validHomeArg(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value && filepath.Base(value) != "." && filepath.Base(value) != string(filepath.Separator)
}

func runHome(ctx context.Context, command attemptCommand, stdout, stderr io.Writer) int {
	var result install.Result
	var err error
	if command.kind == commandInit {
		result, err = install.Init(ctx, command.home)
	} else {
		result, err = install.Doctor(ctx, command.home)
	}
	if err != nil {
		switch {
		case errors.Is(err, install.ErrUncertain):
			_, _ = io.WriteString(stderr, "factoryctl: home publication outcome is uncertain; inspect the explicit home path before retrying\n")
		case errors.Is(err, install.ErrUnsupported):
			_, _ = io.WriteString(stderr, "factoryctl: Go home operations are unsupported on this platform\n")
		case errors.Is(err, install.ErrInvalidHome):
			_, _ = io.WriteString(stderr, "factoryctl: home is invalid or not an exact stopped Go home\n")
		default:
			_, _ = io.WriteString(stderr, "factoryctl: home operation failed\n")
		}
		return exitFailure
	}
	if result.State == install.Ready {
		_, _ = io.WriteString(stdout, "home ready\n")
	} else {
		_, _ = io.WriteString(stdout, "home initialized\n")
	}
	return 0
}

func parseWeb(args []string) (attemptCommand, bool, bool) {
	switch args[1] {
	case "status":
		if len(args) == 2 {
			return attemptCommand{kind: commandWebStatus}, false, true
		}
	case "open":
		if len(args) == 2 {
			return attemptCommand{kind: commandWebOpen}, false, true
		}
	case "list-clients":
		if len(args) == 2 {
			return attemptCommand{kind: commandWebListClients}, false, true
		}
		if len(args) == 4 && args[2] == "--after" && validBrowserClientID(args[3]) {
			return attemptCommand{kind: commandWebListClients, after: args[3]}, false, true
		}
	case "revoke":
		if len(args) == 5 && validBrowserClientID(args[2]) && args[3] == "--revision" {
			revision, ok := parseRevision(args[4])
			if ok {
				return attemptCommand{kind: commandWebRevoke, id: args[2], expectedRevision: revision}, false, true
			}
		}
	}
	return attemptCommand{}, false, false
}

func validBrowserClientID(value string) bool { return validHumanRequestKey(value) }

func parseRevision(value string) (uint64, bool) {
	if value == "" || value[0] == '0' || len(value) > 19 {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return parsed, err == nil && parsed > 0 && parsed <= uint64(^uint64(0)>>1)
}

func helpFlag(value string) bool { return value == "-h" || value == "--help" }

func validHumanRequestKey(value string) bool {
	if len(value) != 32 {
		return false
	}
	nonzero := false
	for _, value := range []byte(value) {
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return false
			}
		}
		nonzero = nonzero || value != '0'
	}
	return nonzero
}

func validQuestion(value string) bool {
	return len(value) >= 1 && len(value) <= 8192 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func writeFailure(stderr io.Writer, kind commandKind, err error) {
	subject, input := "outcome request", "attempt input"
	if kind == commandRequestHuman {
		subject, input = "human request", "human request input"
	}
	message := "factoryctl: " + subject + " failed\n"
	var remote *api.RemoteError
	switch {
	case errors.Is(err, api.ErrInvalidClient):
		message = "factoryctl: attempt client configuration is invalid\n"
	case errors.Is(err, api.ErrInvalidInput):
		message = "factoryctl: " + input + " is invalid\n"
	case errors.Is(err, context.Canceled):
		message = "factoryctl: " + subject + " canceled\n"
	case errors.Is(err, context.DeadlineExceeded):
		message = "factoryctl: " + subject + " timed out\n"
	case errors.As(err, &remote):
		message = "factoryctl: " + subject + " was not accepted\n"
	}
	_, _ = io.WriteString(stderr, message)
}

func runWeb(ctx context.Context, command attemptCommand, getenv func(string) string, stdout, stderr io.Writer, opener browserOpener) int {
	socket := getenv("DARK_FACTORY_SOCKET")
	if socket == "" {
		_, _ = io.WriteString(stderr, "factoryctl: web client configuration is invalid\n")
		return exitFailure
	}
	if command.kind == commandWebStatus {
		if !socketDaemonPresent(socket) {
			return writeJSON(stdout, api.WebStatus{State: "stopped", Ready: false, ProtocolVersion: 1})
		}
	}
	token := getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE")
	if token == "" {
		_, _ = io.WriteString(stderr, "factoryctl: web client configuration is invalid\n")
		return exitFailure
	}
	client, err := api.NewOperatorClient(socket, token)
	if err != nil {
		if command.kind == commandWebStatus && errors.Is(err, api.ErrInvalidClient) {
			return writeJSON(stdout, api.WebStatus{State: "stopped", Ready: false, ProtocolVersion: 1})
		}
		_, _ = io.WriteString(stderr, "factoryctl: web client configuration is invalid\n")
		return exitFailure
	}
	callContext, cancel := context.WithTimeout(ctx, attemptRequestTimeout)
	defer cancel()
	switch command.kind {
	case commandWebStatus:
		result, callErr := client.WebStatus(callContext)
		if callErr != nil {
			if errors.Is(callErr, api.ErrInvalidClient) || errors.Is(callErr, api.ErrTransport) {
				return writeJSON(stdout, api.WebStatus{State: "stopped", Ready: false, ProtocolVersion: 1})
			}
			return writeWebFailure(stderr, "web status", callErr)
		}
		return writeJSON(stdout, result)
	case commandWebOpen:
		result, callErr := client.WebOpen(callContext)
		if callErr != nil {
			return handleWebOpenFailure(client, result, callErr, stderr)
		}
		if !validLaunch(result) {
			return handleWebOpenFailure(client, result, api.ErrProtocol, stderr)
		}
		challengeDigest, _ := exactLaunchDigest(result)
		if opener == nil || opener(callContext, result.LaunchURL) != nil {
			cleanupErr := abandonWebOpen(client, challengeDigest)
			if cleanupErr != nil {
				_, _ = io.WriteString(stderr, "factoryctl: web browser could not be opened; challenge cleanup remains unresolved\n")
			} else {
				_, _ = io.WriteString(stderr, "factoryctl: web browser could not be opened\n")
			}
			return exitFailure
		}
		return writeJSON(stdout, struct {
			State       string `json:"state"`
			ExpiresAtMs uint64 `json:"expires_at_ms"`
		}{State: "opened", ExpiresAtMs: result.ExpiresAtMs})
	case commandWebListClients:
		result, callErr := client.WebListClients(callContext, command.after)
		if callErr != nil {
			return writeWebFailure(stderr, "web list clients", callErr)
		}
		return writeJSON(stdout, result)
	case commandWebRevoke:
		result, callErr := client.WebRevokeClient(callContext, command.id, command.expectedRevision)
		if callErr != nil {
			return writeWebFailure(stderr, "web revoke", callErr)
		}
		return writeJSON(stdout, result)
	default:
		return exitUsage
	}
}

func handleWebOpenFailure(client *api.OperatorClient, launch api.WebLaunch, openErr error, stderr io.Writer) int {
	if challengeDigest, exact := exactLaunchDigest(launch); exact {
		if cleanupErr := abandonWebOpen(client, challengeDigest); cleanupErr == nil {
			return writeWebFailure(stderr, "web open", openErr)
		}
		_, _ = io.WriteString(stderr, "factoryctl: web open failed; challenge cleanup remains unresolved\n")
		return exitFailure
	}

	// A completely empty result paired with a daemon rejection is authoritative
	// and occurs before a launch is minted. Any partial or malformed launch
	// must remain an uncertainty: no cleanup target is safe to choose.
	var remote *api.RemoteError
	if launch == (api.WebLaunch{}) && errors.As(openErr, &remote) {
		return writeWebFailure(stderr, "web open", openErr)
	}
	_, _ = io.WriteString(stderr, "factoryctl: web open failed; challenge cleanup remains unresolved\n")
	return exitFailure
}

func abandonWebOpen(client *api.OperatorClient, challengeDigest string) error {
	cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), attemptRequestTimeout)
	defer cleanupCancel()
	_, err := client.WebAbandonOpen(cleanupContext, challengeDigest)
	return err
}

// socketDaemonPresent distinguishes a missing or stale pathname from a
// daemon that can actually accept a local connection. The probe sends no
// request and is used only by the read-only status command; all other web
// commands still require the authenticated API client.
func socketDaemonPresent(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	connection, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func writeJSON(stdout io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		return exitFailure
	}
	return 0
}

func validLaunch(launch api.WebLaunch) bool {
	_, exact := exactLaunchDigest(launch)
	return exact && launch.ExpiresAtMs > 0 && launch.Outcome == api.WebLaunchReady
}

// exactLaunchDigest proves the only cleanup identity accepted by factoryctl:
// the launch URL is the fixed syntactic form, its fragment carries raw
// challenge A, and the daemon's returned digest is SHA-256(A). No digest is
// returned for an invalid or internally inconsistent launch.
func exactLaunchDigest(launch api.WebLaunch) (string, bool) {
	if len(launch.LaunchURL) < 1 || len(launch.LaunchURL) > 4096 || !utf8.ValidString(launch.LaunchURL) || strings.ContainsRune(launch.LaunchURL, 0) || !strings.HasPrefix(launch.LaunchURL, "https://") || !validHex(launch.ChallengeDigest, 64) {
		return "", false
	}
	parsed, err := url.Parse(launch.LaunchURL)
	if err != nil || parsed.Host != "app.darkfactory.build" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment == "" || parsed.RawFragment != "" || parsed.Path != "/" || !strings.HasPrefix(parsed.Fragment, "df_pair=") {
		return "", false
	}
	raw := strings.TrimPrefix(parsed.Fragment, "df_pair=")
	if !validHex(raw, 64) {
		return "", false
	}
	challenge, err := hex.DecodeString(raw)
	if err != nil || len(challenge) != 32 {
		return "", false
	}
	digest := sha256.Sum256(challenge)
	encoded := hex.EncodeToString(digest[:])
	if encoded != launch.ChallengeDigest {
		return "", false
	}
	return encoded, true
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return false
	}
	return value != strings.Repeat("0", length)
}

func writeWebFailure(stderr io.Writer, subject string, err error) int {
	message := "factoryctl: " + subject + " failed\n"
	var remote *api.RemoteError
	switch {
	case errors.Is(err, context.Canceled):
		message = "factoryctl: " + subject + " canceled\n"
	case errors.Is(err, context.DeadlineExceeded):
		message = "factoryctl: " + subject + " timed out\n"
	case errors.As(err, &remote):
		if remote.Code() == api.RemoteCleanupUnresolved {
			message = "factoryctl: web revoke committed but browser cleanup remains unresolved\n"
		} else {
			message = "factoryctl: " + subject + " was not accepted\n"
		}
	}
	_, _ = io.WriteString(stderr, message)
	return exitFailure
}
