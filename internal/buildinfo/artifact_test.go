package buildinfo

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const fixtureSource = "1234567890abcdef1234567890abcdef12345678"

func TestReleaseArtifactInspectionRejectsWrongIdentityAndArchitecture(t *testing.T) {
	directory := t.TempDir()
	arm, armIdentity := buildFixture(t, directory, "factoryctl-arm", "factoryctl", "1.2.3", fixtureSource, "darwin/arm64", "")
	file, err := os.Open(arm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InspectReleaseArtifact(file, "factoryctl", armIdentity); err != nil {
		t.Fatalf("valid artifact rejected: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	wrongVersion, _ := Expected("1.2.4", fixtureSource, "darwin/arm64")
	assertArtifactRejected(t, arm, "factoryctl", wrongVersion, "linked receipt")
	wrongSource, _ := Expected("1.2.3", "2234567890abcdef1234567890abcdef12345678", "darwin/arm64")
	assertArtifactRejected(t, arm, "factoryctl", wrongSource, "linked receipt")
	intelIdentity, _ := Expected("1.2.3", fixtureSource, "darwin/amd64")
	assertArtifactRejected(t, arm, "factoryctl", intelIdentity, "wrong Mach-O target")
	assertArtifactRejected(t, arm, "factoryd", armIdentity, "wrong Go main package")

	badReceipt := strings.Join([]string{"1.2.3", fixtureSource, "darwin/arm64", strings.Repeat("a", 64)}, "|")
	bad, _ := buildFixture(t, directory, "factoryctl-bad", "factoryctl", "1.2.3", fixtureSource, "darwin/arm64", badReceipt)
	assertArtifactRejected(t, bad, "factoryctl", armIdentity, "linked receipt")
}

func TestSnapshotUsesOpenedObjectWhenSourcePathIsReplaced(t *testing.T) {
	directory := t.TempDir()
	source, identity := buildFixture(t, directory, "factoryctl", "factoryctl", "1.2.3", fixtureSource, "darwin/arm64", "")
	original := filepath.Join(directory, "opened-factoryctl")
	replacement := filepath.Join(directory, "replacement-factoryctl")
	if err := os.Rename(source, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, []byte("not a release binary\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, source); err != nil {
		t.Fatal(err)
	}
	destinationDirectory := filepath.Join(directory, "snapshot")
	if err := os.Mkdir(destinationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(destinationDirectory, "factoryctl")
	_, err := snapshotReleaseArtifact(source, destination, "factoryctl", identity, func() {
		if renameErr := os.Rename(source, original); renameErr != nil {
			t.Fatal(renameErr)
		}
		if renameErr := os.Rename(replacement, source); renameErr != nil {
			t.Fatal(renameErr)
		}
	})
	if err != nil {
		t.Fatalf("opened artifact was not retained: %v", err)
	}
	want, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("snapshot followed the replaced source pathname")
	}
}

func TestArchiveBoundsAreClosed(t *testing.T) {
	if err := ValidateTargetBounds(MaxTargetBytes); err != nil {
		t.Fatalf("exact target bound rejected: %v", err)
	}
	if ValidateTargetBounds(MaxTargetBytes+1) == nil {
		t.Fatal("oversized aggregate target accepted before archiving")
	}
	if err := ValidateArchiveBounds(MaxTargetBytes, MaxArchiveBytes); err != nil {
		t.Fatalf("exact archive bounds rejected: %v", err)
	}
	for _, values := range [][2]int64{
		{0, 1}, {1, 0}, {MaxTargetBytes + 1, MaxArchiveBytes},
		{MaxTargetBytes, MaxArchiveBytes + 1}, {MaxCompressionRatio + 1, 1},
	} {
		if ValidateArchiveBounds(values[0], values[1]) == nil {
			t.Fatalf("invalid archive bounds accepted: %v", values)
		}
	}
	if err := ValidateReleaseArchiveBounds(MaxReleaseArchiveBytes/2, MaxReleaseArchiveBytes/2); err != nil {
		t.Fatalf("exact release bound rejected: %v", err)
	}
	if ValidateReleaseArchiveBounds(MaxReleaseArchiveBytes, 1) == nil {
		t.Fatal("oversized aggregate release accepted")
	}
}

func buildFixture(t *testing.T, directory, output, component, version, source, target, linkedReceipt string) (string, Identity) {
	t.Helper()
	identity, ok := Expected(version, source, target)
	if !ok {
		t.Fatal("invalid fixture identity")
	}
	if linkedReceipt == "" {
		linkedReceipt = identity.Receipt()
	}
	architecture := strings.TrimPrefix(target, "darwin/")
	path := filepath.Join(directory, output)
	command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X github.com/dark-factory-build/dark-factory/internal/buildinfo.receipt="+linkedReceipt, "-o", path, "../../cmd/"+component)
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=darwin", "GOARCH="+architecture, "GOENV=off", "GOAUTH=off", "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	return path, identity
}

func assertArtifactRejected(t *testing.T, path, component string, identity Identity, reason string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := InspectReleaseArtifact(file, component, identity); err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("artifact rejection = %v; want %q on %s/%s", err, reason, runtime.GOOS, runtime.GOARCH)
	}
}
