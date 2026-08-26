package daemon

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestWorkerConfigRoundTripStrictBoundsAndPrivacy(t *testing.T) {
	want := workerConfigFixture()
	encoded, err := encodeWorkerConfig(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeWorkerConfig(encoded)
	if err != nil || !equalWorkerConfig(got, want) {
		t.Fatalf("round trip = %#v, %v", got, err)
	}
	for _, formatted := range []string{fmt.Sprint(want), fmt.Sprintf("%#v", want)} {
		for _, private := range []string{want.RepositoryRoot, want.AttemptTokenPath, string(want.StartupInput)} {
			if strings.Contains(formatted, private) {
				t.Fatalf("format leaked private value: %q", formatted)
			}
		}
	}

	badValues := []workerConfig{
		withConfig(want, func(value *workerConfig) { value.RuntimeIdentity = runner.FileIdentity{} }),
		withConfig(want, func(value *workerConfig) { value.RepositoryIdentity = change.RepositoryIdentity{} }),
		withConfig(want, func(value *workerConfig) { value.RepositoryRoot = "relative" }),
		withConfig(want, func(value *workerConfig) { value.FinalName = ".git" }),
		withConfig(want, func(value *workerConfig) { value.StagingName = value.FinalName }),
		withConfig(want, func(value *workerConfig) { value.AttemptTokenPath = "/outside/token" }),
		withConfig(want, func(value *workerConfig) { value.AttemptSocket = "/" + strings.Repeat("s", maximumSocketBytes) }),
		withConfig(want, func(value *workerConfig) { value.StartupInput = make([]byte, workerInputLimit+1) }),
	}
	for index, bad := range badValues {
		if _, err := encodeWorkerConfig(bad); !errors.Is(err, errInvalidContract) {
			t.Fatalf("bad config %d error = %v", index, err)
		}
	}

	mutations := [][]byte{
		append(bytes.Clone(encoded), 0),
		bytes.Clone(encoded[:len(encoded)-1]),
		bytes.Clone(encoded), bytes.Clone(encoded), bytes.Clone(encoded), bytes.Clone(encoded),
	}
	mutations[2][0] ^= 1
	mutations[3][4]++
	mutations[4][5] = byte(kindSelection)
	mutations[5][7] = 1
	for index, bad := range mutations {
		if _, err := decodeWorkerConfig(bad); !errors.Is(err, errInvalidContract) {
			t.Fatalf("wire mutation %d error = %v", index, err)
		}
	}
}

func TestClosedConfigAndCheckpointShapesExcludeAuthorityFields(t *testing.T) {
	configFields := fieldsOf(workerConfig{})
	if configFields["Bearer"] || configFields["AttemptToken"] || !configFields["AttemptTokenPath"] {
		t.Fatalf("worker config bearer surface = %v", configFields)
	}
	for _, checkpoint := range []any{selectionCheckpoint{}, preparationCheckpoint{}, populationCheckpoint{}} {
		fields := fieldsOf(checkpoint)
		for _, forbidden := range []string{"Bearer", "AttemptToken", "AttemptTokenPath", "ProviderInput", "StartupInput"} {
			if fields[forbidden] {
				t.Fatalf("%T exposes %s", checkpoint, forbidden)
			}
		}
	}
}

func TestCheckpointGoldenRoundTripsAndFieldBinding(t *testing.T) {
	selection := selectionFixture(t)
	selectionBytes, err := encodeSelectionCheckpoint(selection)
	if err != nil {
		t.Fatal(err)
	}
	const selectionGolden = "4446444301020000010101010101010101010101010101010101010101070707070707070707070707070707070707070707070707070707070707070700000000000000070000000000000063000000000000000b000000000000000c"
	if got := hex.EncodeToString(selectionBytes); got != selectionGolden {
		t.Fatalf("selection golden = %s", got)
	}
	decodedSelection, err := decodeSelectionCheckpoint(selectionBytes)
	if err != nil || !equalSelection(decodedSelection, selection) {
		t.Fatalf("selection round trip = %#v, %v", decodedSelection, err)
	}

	preparation := preparationCheckpoint{Stage: mustStageIdentity(t, 13, 14)}
	preparationBytes, err := encodePreparationCheckpoint(preparation)
	if err != nil {
		t.Fatal(err)
	}
	const preparationGolden = "4446444301030000000000000000000d000000000000000e"
	if got := hex.EncodeToString(preparationBytes); got != preparationGolden {
		t.Fatalf("preparation golden = %s", got)
	}
	decodedPreparation, err := decodePreparationCheckpoint(preparationBytes)
	if err != nil || !decodedPreparation.Stage.Equal(preparation.Stage) {
		t.Fatalf("preparation round trip = %#v, %v", decodedPreparation, err)
	}

	population := populationCheckpoint{Identity: mustStageIdentity(t, 13, 14), Commitment: selection.Commitment, EntryCount: 7, BlobBytes: 99}
	populationBytes, err := encodePopulationCheckpoint(population)
	if err != nil {
		t.Fatal(err)
	}
	const populationGolden = "4446444301040000000000000000000d000000000000000e070707070707070707070707070707070707070707070707070707070707070700000000000000070000000000000063"
	if got := hex.EncodeToString(populationBytes); got != populationGolden {
		t.Fatalf("population golden = %s", got)
	}
	decodedPopulation, err := decodePopulationCheckpoint(populationBytes)
	if err != nil || !equalPopulation(decodedPopulation, population) {
		t.Fatalf("population round trip = %#v, %v", decodedPopulation, err)
	}

	bound := bytes.Clone(selectionBytes)
	// Entry count is the uint64 immediately before blob bytes and repository identity.
	entryOffset := len(bound) - 32
	bound[entryOffset+7] = 8
	changed, err := decodeSelectionCheckpoint(bound)
	if err != nil || changed.EntryCount != 8 || changed.BlobBytes != selection.BlobBytes {
		t.Fatalf("selection fields rebound: %#v, %v", changed, err)
	}
}

func TestCheckpointDecoderRejectsEveryMalformedBoundary(t *testing.T) {
	selectionBytes, err := encodeSelectionCheckpoint(selectionFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for cut := 0; cut < len(selectionBytes); cut++ {
		if _, err := decodeSelectionCheckpoint(selectionBytes[:cut]); !errors.Is(err, errInvalidContract) {
			t.Fatalf("truncation %d error = %v", cut, err)
		}
	}
	for name, mutate := range map[string]func([]byte){
		"unknown kind":   func(value []byte) { value[5] = 99 },
		"unknown format": func(value []byte) { value[8] = 99 },
		"reserved":       func(value []byte) { value[6] = 1 },
		"entry overflow": func(value []byte) { value[len(value)-32] = 0xff },
		"blob overflow":  func(value []byte) { value[len(value)-24] = 0xff },
		"repository":     func(value []byte) { clear(value[len(value)-8:]) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := bytes.Clone(selectionBytes)
			mutate(bad)
			if _, err := decodeSelectionCheckpoint(bad); !errors.Is(err, errInvalidContract) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := decodeSelectionCheckpoint(append(bytes.Clone(selectionBytes), 0)); !errors.Is(err, errInvalidContract) {
		t.Fatalf("trailing error = %v", err)
	}
	if _, err := decodeSelectionCheckpoint(make([]byte, checkpointLimit+1)); !errors.Is(err, errInvalidContract) {
		t.Fatalf("oversize error = %v", err)
	}
}

func workerConfigFixture() workerConfig {
	repository, err := change.NewRepositoryIdentity(3, 4)
	if err != nil {
		panic(err)
	}
	return workerConfig{
		RuntimePath: "/private/runtime/run-1", RuntimeIdentity: runner.FileIdentity{Device: 1, Inode: 2},
		GitExecutable: "/usr/bin/git", RepositoryRoot: "/private/repository", RepositoryIdentity: repository, Revision: "refs/heads/main",
		ChangeParent: "/private/changes", FinalName: "change-1", StagingName: ".change-1.stage",
		ProviderHome: "/private/runtime/run-1/home", ProviderTemp: "/private/runtime/run-1/tmp",
		AttemptSocket: "/private/api.sock", AttemptTokenPath: "/private/runtime/run-1/attempt.token", StartupInput: []byte("private prompt\n"),
	}
}

func withConfig(value workerConfig, mutate func(*workerConfig)) workerConfig {
	value.StartupInput = bytes.Clone(value.StartupInput)
	mutate(&value)
	return value
}

func equalWorkerConfig(left, right workerConfig) bool {
	return left.RuntimePath == right.RuntimePath && left.RuntimeIdentity == right.RuntimeIdentity && left.GitExecutable == right.GitExecutable &&
		left.RepositoryRoot == right.RepositoryRoot && left.RepositoryIdentity.Equal(right.RepositoryIdentity) && left.Revision == right.Revision && left.ChangeParent == right.ChangeParent &&
		left.FinalName == right.FinalName && left.StagingName == right.StagingName &&
		left.ProviderHome == right.ProviderHome && left.ProviderTemp == right.ProviderTemp && left.AttemptSocket == right.AttemptSocket &&
		left.AttemptTokenPath == right.AttemptTokenPath && bytes.Equal(left.StartupInput, right.StartupInput)
}

func selectionFixture(t testing.TB) selectionCheckpoint {
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
	return selectionCheckpoint{Format: format, Base: base, Commitment: commitment, EntryCount: 7, BlobBytes: 99, Repository: repository}
}

func equalSelection(left, right selectionCheckpoint) bool {
	return left.Format.Name() == right.Format.Name() && left.Base.Hex() == right.Base.Hex() && left.Commitment.Equal(right.Commitment) &&
		left.EntryCount == right.EntryCount && left.BlobBytes == right.BlobBytes && left.Repository.Equal(right.Repository)
}

func equalPopulation(left, right populationCheckpoint) bool {
	return left.Identity.Equal(right.Identity) && left.Commitment.Equal(right.Commitment) && left.EntryCount == right.EntryCount && left.BlobBytes == right.BlobBytes
}

func fieldsOf(value any) map[string]bool {
	typeOf := reflect.TypeOf(value)
	fields := make(map[string]bool, typeOf.NumField())
	for index := 0; index < typeOf.NumField(); index++ {
		fields[typeOf.Field(index).Name] = true
	}
	return fields
}
