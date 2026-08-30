package daemon

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestBirthEncodingIsExactReversibleAndReserved(t *testing.T) {
	birth := runner.Birth{Seconds: 0x0102030405060708, Microseconds: 999_999}
	encoded, err := encodeBirth(birth)
	if err != nil {
		t.Fatal(err)
	}
	const golden = "44464249525448010102030405060708000f423f000000000000000000000000"
	if got := hex.EncodeToString(encoded[:]); got != golden {
		t.Fatalf("birth golden = %s, want %s", got, golden)
	}
	decoded, err := decodeBirth(encoded)
	if err != nil || decoded != birth {
		t.Fatalf("birth round trip = %+v, %v", decoded, err)
	}

	mutations := map[string]func(*[32]byte){
		"magic":        func(value *[32]byte) { value[0] ^= 1 },
		"version":      func(value *[32]byte) { value[7]++ },
		"reserved":     func(value *[32]byte) { value[31] = 1 },
		"zero seconds": func(value *[32]byte) { clear(value[8:16]) },
		"usec range":   func(value *[32]byte) { value[16], value[17], value[18], value[19] = 0, 0x0f, 0x42, 0x40 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := encoded
			mutate(&bad)
			if _, err := decodeBirth(bad); !errors.Is(err, errInvalidContract) {
				t.Fatalf("decode error = %v", err)
			}
		})
	}
	if _, err := encodeBirth(runner.Birth{}); !errors.Is(err, errInvalidContract) {
		t.Fatalf("zero birth error = %v", err)
	}
}

func TestRunnerProcessIdentityRoundTripsWithoutHashing(t *testing.T) {
	want := runner.Identity{PID: 101, PGID: 101, Birth: runner.Birth{Seconds: 1234567, Microseconds: 765432}}
	resource, err := processResourceIdentity(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runnerIdentity(resource)
	if err != nil || got != want {
		t.Fatalf("round trip = %+v, %v", got, err)
	}
	_, _, digest, ok := resource.Process()
	if !ok {
		t.Fatal("process identity missing")
	}
	encoded, _ := encodeBirth(want.Birth)
	if got := digest.Bytes(); string(got) != string(encoded[:]) {
		t.Fatalf("birth bytes were transformed: %x", got)
	}
}

func TestIdentityConversionsRejectOverflowsAndWrongKinds(t *testing.T) {
	for _, identity := range []runner.FileIdentity{
		{}, {Device: 0, Inode: 1}, {Device: math.MaxUint64, Inode: 1}, {Device: 1, Inode: math.MaxUint64},
	} {
		if _, err := pathResourceIdentity(identity); !errors.Is(err, errInvalidContract) {
			t.Fatalf("path identity %+v error = %v", identity, err)
		}
	}
	file, err := kernel.NewFileIdentity(1, 2)
	if err != nil {
		t.Fatal(err)
	}
	path, err := kernel.NewPathResourceIdentity(file.Device(), file.Inode())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runnerIdentity(path); !errors.Is(err, errInvalidContract) {
		t.Fatalf("path converted to process: %v", err)
	}
	if _, err := processResourceIdentity(runner.Identity{PID: 1, PGID: 1, Birth: runner.Birth{Seconds: 1}}); !errors.Is(err, errInvalidContract) {
		t.Fatalf("weak process identity error = %v", err)
	}
}

func TestCheckpointConversionsBindExactStoreFacts(t *testing.T) {
	result, repository := workerResultFixture(t)
	durable, err := kernelSelectionCheckpoint(result, repository)
	if err != nil {
		t.Fatal(err)
	}
	if durable.ObjectFormat().String() != result.Format.Name() ||
		string(durable.Commit().Bytes()) != string(result.Base.Bytes()) || string(durable.Commitment().Bytes()) != string(result.Commitment.Bytes()) ||
		durable.EntryCount() != uint32(result.EntryCount) || durable.TotalBytes() != result.BlobBytes ||
		durable.RepositoryIdentity().Device() != int64(repository.Device()) || durable.RepositoryIdentity().Inode() != int64(repository.Inode()) {
		t.Fatalf("selection facts were rebound: %+v", durable)
	}
	format, base, stage, err := inspectPublishedArguments(durable, mustKernelFileIdentity(t, 13, 14))
	if err != nil || format.Name() != result.Format.Name() || base.Hex() != result.Base.Hex() || stage.Device() != 13 || stage.Inode() != 14 {
		t.Fatalf("InspectPublished arguments = %s %s %+v, %v", format.Name(), base.Hex(), stage, err)
	}
}

func workerResultFixture(t testing.TB) (changeworker.Result, change.RepositoryIdentity) {
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
	return changeworker.Result{Format: format, Base: base, Commitment: commitment, EntryCount: 7, BlobBytes: 99, Tree: mustStageIdentity(t, 21, 22)}, repository
}

func mustKernelFileIdentity(t testing.TB, device, inode int64) kernel.FileIdentity {
	t.Helper()
	identity, err := kernel.NewFileIdentity(device, inode)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func mustStageIdentity(t testing.TB, device, inode uint64) change.StageIdentity {
	t.Helper()
	identity, err := change.NewStageIdentity(device, inode)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
