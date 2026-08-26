package changeworker

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
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
		for _, sentinel := range []string{want.RuntimePath, want.RepositoryRoot, string(want.StartupInput)} {
			if strings.Contains(formatted, sentinel) {
				t.Fatalf("private value leaked: %q", formatted)
			}
		}
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
		func(v *Config) { v.RuntimeIdentity = runner.FileIdentity{} },
		func(v *Config) { v.RepositoryRoot = "relative" },
		func(v *Config) { v.FinalName = ".GiT" },
		func(v *Config) { v.StagingName = v.FinalName },
		func(v *Config) { v.AttemptSocket = "/" + strings.Repeat("s", maximumSocketBytes) },
		func(v *Config) { v.StartupInput = []byte{0xff} },
		func(v *Config) { v.StartupInput = []byte{'x', 0} },
		func(v *Config) { v.StartupInput = make([]byte, InputLimit+1) },
	}
	for i, mutate := range mutations {
		bad := want
		bad.StartupInput = bytes.Clone(want.StartupInput)
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
	return Config{RuntimePath: "/private/runtime", RuntimeIdentity: runner.FileIdentity{Device: 1, Inode: 2}, GitExecutable: "/Library/Developer/CommandLineTools/usr/bin/git", RepositoryRoot: "/private/repository", RepositoryIdentity: repository, Revision: "main", ChangeParent: "/private/changes", FinalName: "change", StagingName: ".change.stage", AttemptSocket: "/private/api.sock", StartupInput: []byte("printf exact")}
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
