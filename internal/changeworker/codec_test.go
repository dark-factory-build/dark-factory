package changeworker

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestConfigRoundTripIsExactBoundedAndPrivate(t *testing.T) {
	want := configFixture(t)
	encoded, err := EncodeConfig(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeConfig(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed config")
	}
	encoded[0] ^= 1
	if _, err := DecodeConfig(encoded); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("corrupt JSON: %v", err)
	}
	for _, value := range []any{want, Result{}} {
		formatted := fmt.Sprintf("%v %+v %#v", value, value, value)
		for _, sentinel := range []string{want.RuntimePath, want.FactoryctlExecutable, want.RepositoryRoot, string(want.ProviderTask)} {
			if strings.Contains(formatted, sentinel) {
				t.Fatalf("private value leaked: %q", formatted)
			}
		}
	}
}

func TestRetainedConfigRoundTripPreservesExactPublicationAuthority(t *testing.T) {
	want := configFixture(t)
	retained := resultFixture(t)
	want.Retained = &retained
	encoded, err := EncodeConfig(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeConfig(encoded)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("retained config round trip changed authority: %v", err)
	}
	bad := want
	bad.Retained = &Result{}
	if _, err := EncodeConfig(bad); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("invalid retained authority encoded: %v", err)
	}

}

func TestRetainedSourceReviewConfigRoundTripPreservesReceiptIdentity(t *testing.T) {
	want := configFixture(t)
	want.RetainedSourceReview = &SourceReview{TaskID: "task", ChangeID: "change", TaskWorkRevision: 3, ChangeRevision: 7, BaseCommit: "base", HeadCommit: "head", SourcePath: "/private/producer-change", GitDirectory: "/private/producer-repo/.git"}
	encoded, err := EncodeConfig(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeConfig(encoded)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("source review config changed: %v", err)
	}
}

// An orchestrator's config names no Change: its names and retained result
// are empty, and a worker's may not be.
func TestOrchestratorConfigCarriesNoChange(t *testing.T) {
	config := configFixture(t)
	config.Role, config.FinalName = kernel.RoleOrchestrator, ""
	config.PreviousWorkingDirectory = "/private/previous-runtime-home/home"
	encoded, err := EncodeConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeConfig(encoded)
	if err != nil || !reflect.DeepEqual(got, config) {
		t.Fatalf("orchestrator config round trip = %v", err)
	}
	named := config
	named.FinalName = "published"
	retained := resultFixture(t)
	withChange := config
	withChange.Retained = &retained
	worker := configFixture(t)
	worker.FinalName = ""
	workerWithHint := configFixture(t)
	workerWithHint.PreviousWorkingDirectory = config.PreviousWorkingDirectory
	for name, bad := range map[string]Config{"orchestrator with a name": named, "orchestrator with a retained tree": withChange, "worker without a name": worker, "worker with a previous-working-directory hint": workerWithHint} {
		if _, err := EncodeConfig(bad); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("%s encoded: %v", name, err)
		}
	}
	noRole := bytes.Replace(encoded, []byte(`"role":"orchestrator"`), []byte(`"role":"overseer"`), 1)
	if _, err := DecodeConfig(noRole); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("unknown role decoded: %v", err)
	}
}

func TestCodexConfigCarriesNoTaskBytes(t *testing.T) {
	config := configFixture(t)
	config.Provider = kernel.ProviderCodex
	config.ProviderTask = nil
	encoded, err := EncodeConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("printf exact")) {
		t.Fatal("Codex worker config contains private task text")
	}
	got, err := DecodeConfig(encoded)
	if err != nil || got.Provider != kernel.ProviderCodex || len(got.ProviderTask) != 0 {
		t.Fatalf("Codex config round trip = provider %s task %d bytes, err %v", got.Provider, len(got.ProviderTask), err)
	}
	config.ProviderTask = []byte("private task")
	if _, err := EncodeConfig(config); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("Codex worker accepted task bytes: %v", err)
	}
}

func TestConfigStrictJSONRejectsOversizeUnknownTrailingMissingAndInvalidProvider(t *testing.T) {
	want := configFixture(t)
	encoded, err := EncodeConfig(want)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(encoded, []byte{'{'}, []byte(`{"unknown":true,`), 1)
	invalidProvider := bytes.Replace(encoded, []byte(`"provider":"shell"`), []byte(`"provider":"unknown"`), 1)
	if bytes.Equal(invalidProvider, encoded) {
		t.Fatal("provider fixture was not replaced")
	}
	invalidPath := bytes.Replace(encoded, []byte(`"repository_root":"/private/repository"`), []byte(`"repository_root":"relative"`), 1)
	if bytes.Equal(invalidPath, encoded) {
		t.Fatal("path fixture was not replaced")
	}
	for name, value := range map[string][]byte{
		"oversize":         bytes.Repeat([]byte{' '}, ConfigLimit+1),
		"unknown field":    unknown,
		"trailing data":    append(bytes.Clone(encoded), []byte(`{}`)...),
		"missing required": []byte(`{}`),
		"invalid provider": invalidProvider,
		"invalid path":     invalidPath,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeConfig(value); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("malformed config accepted: %v", err)
			}
		})
	}
}

func TestResultRoundTripIsStrictBoundedAndPrivate(t *testing.T) {
	want := resultFixture(t)
	encoded, err := EncodeResult(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeResult(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != want.Format || got.Base.Hex() != want.Base.Hex() || got.Head == nil || got.Head.Hex() != want.Head.Hex() {
		t.Fatal("round trip changed result")
	}
	// A Git-free Change has no head yet; the wire says so by omission.
	legacy := Result{Format: want.Format, Base: want.Base}
	encoded, err = EncodeResult(legacy)
	if err != nil || strings.Contains(string(encoded), "head") {
		t.Fatalf("headless result = %s, %v", encoded, err)
	}
	if got, err := DecodeResult(encoded); err != nil || got.Head != nil || got.Base.Hex() != want.Base.Hex() {
		t.Fatalf("headless round trip = %+v, %v", got, err)
	}
	formatted := fmt.Sprintf("%v %+v %#v", want, want, want)
	for _, private := range []string{want.Base.Hex(), want.Head.Hex()} {
		if strings.Contains(formatted, private) {
			t.Fatalf("private result leaked: %q", formatted)
		}
	}
}

func TestResultRejectsOversizeUnknownTrailingMissingInvalidAndPartialJSON(t *testing.T) {
	encoded, err := EncodeResult(resultFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(encoded, []byte{'{'}, []byte(`{"unknown":true,`), 1)
	missingBase := bytes.Replace(encoded, []byte(`"base":"`+strings.Repeat("01", 20)+`",`), nil, 1)
	if bytes.Equal(missingBase, encoded) {
		t.Fatal("base fixture was not removed")
	}
	invalidFormat := bytes.Replace(encoded, []byte(`"format":"sha1"`), []byte(`"format":"sha512"`), 1)
	if bytes.Equal(invalidFormat, encoded) {
		t.Fatal("format fixture was not replaced")
	}
	shortHead := bytes.Replace(encoded, []byte(`"head":"`+strings.Repeat("07", 20)+`"`), []byte(`"head":"`+strings.Repeat("07", 19)+`"`), 1)
	if bytes.Equal(shortHead, encoded) {
		t.Fatal("head fixture was not replaced")
	}
	upperHead := bytes.Replace(encoded, []byte(`"head":"`+strings.Repeat("07", 20)+`"`), []byte(`"head":"`+strings.Repeat("0A", 20)+`"`), 1)
	for name, value := range map[string][]byte{
		"oversize":         bytes.Repeat([]byte{' '}, ResultLimit+1),
		"unknown field":    unknown,
		"trailing data":    append(bytes.Clone(encoded), []byte(`{}`)...),
		"missing required": missingBase,
		"invalid format":   invalidFormat,
		"short head":       shortHead,
		"upper-case head":  upperHead,
		"partial":          bytes.Clone(encoded[:len(encoded)-1]),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeResult(value); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("malformed result accepted: %v", err)
			}
		})
	}
}

func TestConfigRejectsMalformedRetainedChangeJSON(t *testing.T) {
	want := configFixture(t)
	result := resultFixture(t)
	want.Retained = &result
	encoded, err := EncodeConfig(want)
	if err != nil {
		t.Fatal(err)
	}
	malformed := bytes.Replace(encoded, []byte(`"head":"`+strings.Repeat("07", 20)+`"`), []byte(`"head":"not-a-commit"`), 1)
	if bytes.Equal(malformed, encoded) {
		t.Fatal("retained head fixture was not replaced")
	}
	if _, err := DecodeConfig(malformed); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("malformed retained Change accepted: %v", err)
	}
}

func TestConfigRejectsRawAuthorityAndInputCorruption(t *testing.T) {
	want := configFixture(t)
	mutations := []func(*Config){
		func(v *Config) { v.Provider = kernel.Provider(255) },
		func(v *Config) { v.Provider, v.Model = kernel.ProviderCodex, string([]byte{0xff}) },
		func(v *Config) { v.Provider, v.Model = kernel.ProviderCodex, "model\x00suffix" },
		func(v *Config) { v.ReasoningEffort = strings.Repeat("x", 33) },
		func(v *Config) { v.RuntimeIdentity = runner.FileIdentity{} },
		func(v *Config) { v.AgentID = "" },
		func(v *Config) { v.TaskIncarnationID = "" },
		func(v *Config) { v.AgentID = strings.Repeat("x", maximumSessionKeyPartBytes+1) },
		func(v *Config) { v.PreviousWorkingDirectory = "/private/previous" }, // worker carrying an orchestrator-only hint
		func(v *Config) { // and, for an otherwise-valid orchestrator, a malformed one
			v.Role, v.FinalName, v.PreviousWorkingDirectory = kernel.RoleOrchestrator, "", "relative"
		},
		func(v *Config) { v.RepositoryRoot = "relative" },
		func(v *Config) { v.FactoryctlExecutable = "" },
		func(v *Config) { v.FactoryctlExecutable = "relative/factoryctl" },
		func(v *Config) { v.FactoryctlExecutable = "/private/../factoryctl" },
		func(v *Config) { v.FactoryctlExecutable = "/private/factoryctl\x00foreign" },
		func(v *Config) { v.FactoryctlExecutable = "/" + strings.Repeat("f", maximumLocatorBytes) },
		func(v *Config) { v.ToolPath = "relative:/usr/bin" },
		func(v *Config) { v.LocalCILeaseDir = "/private/repository/.git" },
		func(v *Config) { v.ToolchainReadRoots = v.RuntimePath },
		func(v *Config) { v.ToolchainReadRoots = v.AccountHome },
		func(v *Config) { v.ToolchainReadRoots = v.RepositoryRoot },
		func(v *Config) { v.Provider, v.ReasoningEffort = kernel.ProviderCodex, "speculative" },
		func(v *Config) { v.FinalName = ".GiT" },
		func(v *Config) { v.GitCommonDir = "/private/other/.git" },
		func(v *Config) { v.GitCommonDir = v.RepositoryRoot },
		func(v *Config) { v.GitCommonDir = "" },
		func(v *Config) { v.AttemptSocket = "/" + strings.Repeat("s", install.MaxSocketPathBytes) },
		func(v *Config) { v.ProviderTask = []byte{0xff} },
		func(v *Config) { v.ProviderTask = []byte{'x', 0} },
		func(v *Config) { v.ProviderTask = nil },
		func(v *Config) { v.ProviderTask = make([]byte, runner.MaxProviderTaskBytes+1) },
	}
	for i, mutate := range mutations {
		bad := want
		bad.ProviderTask = bytes.Clone(want.ProviderTask)
		mutate(&bad)
		if _, err := EncodeConfig(bad); !errors.Is(err, ErrInvalidContract) {
			t.Fatalf("mutation %d accepted: %v", i, err)
		}
	}
	fields := reflect.VisibleFields(reflect.TypeOf(Config{}))
	for _, forbidden := range []string{"ProviderHome", "ProviderTemp", "AttemptTokenPath", "ProviderProgram"} {
		for _, field := range fields {
			if field.Name == forbidden {
				t.Fatalf("raw authority field retained: %s", forbidden)
			}
		}
	}
}

func configFixture(t testing.TB) Config {
	t.Helper()
	repository, err := change.NewRepositoryIdentity(11, 12)
	if err != nil {
		t.Fatal(err)
	}
	return Config{Provider: kernel.ProviderShell, Role: kernel.RoleWorker, AgentID: "agent-fixture", TaskIncarnationID: "incarnation-fixture", RuntimePath: "/private/runtime", RuntimeIdentity: runner.FileIdentity{Device: 1, Inode: 2}, GitExecutable: "/Library/Developer/CommandLineTools/usr/bin/git", FactoryctlExecutable: "/private/release/factoryctl", ToolPath: "/opt/homebrew/bin:/usr/bin:/bin", ToolchainReadRoots: "/opt/software/node:/opt/software/go", LocalCILeaseDir: "/private/repository/.git/dark-factory-local-ci", AccountHome: "/private/account", RepositoryRoot: "/private/repository", RepositoryIdentity: repository, GitCommonDir: "/private/repository/.git", Revision: "main", ChangeParent: "/private/changes", FinalName: "change", AttemptSocket: "/private/api.sock", ProviderTask: []byte("printf exact")}
}

func resultFixture(t testing.TB) Result {
	t.Helper()
	format, err := change.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := change.NewObjectID(format, bytes.Repeat([]byte{1}, format.OIDLength()))
	if err != nil {
		t.Fatal(err)
	}
	head, err := change.NewObjectID(format, bytes.Repeat([]byte{7}, format.OIDLength()))
	if err != nil {
		t.Fatal(err)
	}
	return Result{Format: format, Base: base, Head: &head}
}
