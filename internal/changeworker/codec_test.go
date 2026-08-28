package changeworker

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
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
		t.Fatalf("corrupt magic: %v", err)
	}
	for _, value := range []any{want, SelectionReport{}, PreparationReport{}, PopulationReport{}} {
		formatted := fmt.Sprintf("%v %+v %#v", value, value, value)
		for _, sentinel := range []string{want.RuntimePath, want.FactoryctlExecutable, want.RepositoryRoot, string(want.InitialTerminalInput)} {
			if strings.Contains(formatted, sentinel) {
				t.Fatalf("private value leaked: %q", formatted)
			}
		}
	}
}

func TestConfigV1HardCutoverRejectsOldUnknownTrailingAndDuplicateLocator(t *testing.T) {
	want := configFixture(t)
	encoded, err := EncodeConfig(want)
	if err != nil {
		t.Fatal(err)
	}
	locatorStart, locatorEnd := configStringBounds(t, encoded, 2)
	old := append(bytes.Clone(encoded[:locatorStart]), encoded[locatorEnd:]...)
	unknown := bytes.Clone(encoded)
	unknown[4]++
	trailing := append(bytes.Clone(encoded), 0)
	duplicate := append(bytes.Clone(encoded[:locatorEnd]), encoded[locatorStart:locatorEnd]...)
	duplicate = append(duplicate, encoded[locatorEnd:]...)
	for name, value := range map[string][]byte{"old": old, "unknown": unknown, "trailing": trailing, "duplicate locator": duplicate} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeConfig(value); !errors.Is(err, ErrInvalidContract) {
				t.Fatalf("foreign v1 config accepted: %v", err)
			}
		})
	}
}

func TestReportsRoundTripAndRejectTrailingBytes(t *testing.T) {
	selection := selectionFixture(t)
	encoded, err := EncodeSelectionReport(selection)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeSelectionReport(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != selection.Format || got.Base.Hex() != selection.Base.Hex() || !got.Commitment.Equal(selection.Commitment) || got.EntryCount != selection.EntryCount || got.BlobBytes != selection.BlobBytes || !got.Repository.Equal(selection.Repository) {
		t.Fatal("selection report changed")
	}
	if _, err := DecodeSelectionReport(append(encoded, 0)); !errors.Is(err, ErrInvalidContract) {
		t.Fatalf("trailing selection accepted: %v", err)
	}
	preparation := PreparationReport{Stage: mustStage(t, 13, 14)}
	encoded, err = EncodePreparationReport(preparation)
	if err != nil {
		t.Fatal(err)
	}
	decodedPreparation, err := DecodePreparationReport(encoded)
	if err != nil || !decodedPreparation.Stage.Equal(preparation.Stage) {
		t.Fatalf("preparation=%+v err=%v", decodedPreparation, err)
	}
	population := PopulationReport{Identity: preparation.Stage, Commitment: selection.Commitment, EntryCount: 7, BlobBytes: 99}
	encoded, err = EncodePopulationReport(population)
	if err != nil {
		t.Fatal(err)
	}
	decodedPopulation, err := DecodePopulationReport(encoded)
	if err != nil || !decodedPopulation.Identity.Equal(population.Identity) || !decodedPopulation.Commitment.Equal(population.Commitment) {
		t.Fatalf("population=%+v err=%v", decodedPopulation, err)
	}
}

func TestConfigRejectsRawAuthorityAndInputCorruption(t *testing.T) {
	want := configFixture(t)
	mutations := []func(*Config){
		func(v *Config) { v.Provider = kernel.Provider(255) },
		func(v *Config) { v.Model = string([]byte{0xff}) },
		func(v *Config) { v.ReasoningEffort = strings.Repeat("x", 33) },
		func(v *Config) { v.RuntimeIdentity = runner.FileIdentity{} },
		func(v *Config) { v.RepositoryRoot = "relative" },
		func(v *Config) { v.FactoryctlExecutable = "" },
		func(v *Config) { v.FactoryctlExecutable = "relative/factoryctl" },
		func(v *Config) { v.FactoryctlExecutable = "/private/../factoryctl" },
		func(v *Config) { v.FactoryctlExecutable = "/private/factoryctl\x00foreign" },
		func(v *Config) { v.FactoryctlExecutable = "/" + strings.Repeat("f", maximumLocatorBytes) },
		func(v *Config) { v.ToolPath = "relative:/usr/bin" },
		func(v *Config) { v.Provider, v.ReasoningEffort = kernel.ProviderCodex, "speculative" },
		func(v *Config) { v.FinalName = ".GiT" },
		func(v *Config) { v.StagingName = v.FinalName },
		func(v *Config) { v.AttemptSocket = "/" + strings.Repeat("s", maximumSocketBytes) },
		func(v *Config) { v.InitialTerminalInput = []byte{0xff} },
		func(v *Config) { v.InitialTerminalInput = []byte{'x', 0} },
		func(v *Config) { v.InitialTerminalInput = nil },
		func(v *Config) { v.InitialTerminalInput = make([]byte, runner.MaxProviderInputBytes+1) },
	}
	for i, mutate := range mutations {
		bad := want
		bad.InitialTerminalInput = bytes.Clone(want.InitialTerminalInput)
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
	return Config{Provider: kernel.ProviderShell, RuntimePath: "/private/runtime", RuntimeIdentity: runner.FileIdentity{Device: 1, Inode: 2}, GitExecutable: "/Library/Developer/CommandLineTools/usr/bin/git", FactoryctlExecutable: "/private/release/factoryctl", ToolPath: "/opt/homebrew/bin:/usr/bin:/bin", RepositoryRoot: "/private/repository", RepositoryIdentity: repository, Revision: "main", ChangeParent: "/private/changes", FinalName: "change", StagingName: ".change.stage", AttemptSocket: "/private/api.sock", InitialTerminalInput: []byte("printf exact")}
}

func configStringBounds(t testing.TB, encoded []byte, want int) (int, int) {
	t.Helper()
	offset := 8 + 4*8 + 1
	for index := 0; index <= want; index++ {
		if offset+2 > len(encoded) {
			t.Fatal("short encoded config")
		}
		start := offset
		length := int(binary.BigEndian.Uint16(encoded[offset : offset+2]))
		offset += 2 + length
		if offset > len(encoded) {
			t.Fatal("invalid encoded config string")
		}
		if index == want {
			return start, offset
		}
	}
	return 0, 0
}

func selectionFixture(t testing.TB) SelectionReport {
	t.Helper()
	format, err := change.NewObjectFormat("sha1")
	if err != nil {
		t.Fatal(err)
	}
	base, err := change.NewObjectID(format, bytes.Repeat([]byte{1}, format.OIDLength()))
	if err != nil {
		t.Fatal(err)
	}
	commitment, err := change.ParseCommitment(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := change.NewRepositoryIdentity(11, 12)
	if err != nil {
		t.Fatal(err)
	}
	return SelectionReport{Format: format, Base: base, Commitment: commitment, EntryCount: 7, BlobBytes: 99, Repository: repository}
}

func mustStage(t testing.TB, device, inode uint64) change.StageIdentity {
	t.Helper()
	result, err := change.NewStageIdentity(device, inode)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
