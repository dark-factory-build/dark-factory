//go:build darwin

package install

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
)

const serviceFixtureSource = "1234567890abcdef1234567890abcdef12345678"

func TestServiceBundleRetainsExactlyThreeMatchingReleaseArtifacts(t *testing.T) {
	root := serviceTestRoot(t)
	source, identity := buildServiceBundleFixture(t, root, "1.2.3", serviceFixtureSource)
	baselineFD := serviceFDCount(t)
	bundle, err := OpenServiceBundle(filepath.Join(source, "factoryctl"), identity)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bundle.String(), source) || strings.Contains(bundle.GoString(), identity.BuildID()) {
		t.Fatal("bundle formatting exposed private identity")
	}
	stage := filepath.Join(root, "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(stage)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range serviceBundleComponentNames() {
		if err := bundle.Snapshot(parent, component); err != nil {
			t.Fatalf("snapshot %s: %v", component, err)
		}
		file, err := os.Open(filepath.Join(stage, component))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := buildinfo.InspectReleaseArtifact(file, component, identity); err != nil {
			_ = file.Close()
			t.Fatalf("inspect snapshot %s: %v", component, err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := serviceFDCount(t); got != baselineFD {
		t.Fatalf("FD count = %d, want %d", got, baselineFD)
	}
	if err := bundle.Snapshot(nil, "factoryd"); !errors.Is(err, ErrClosed) {
		t.Fatalf("snapshot after close = %v", err)
	}
}

func TestServiceBundleRejectsAuthorityAndIdentityMutations(t *testing.T) {
	root := serviceTestRoot(t)
	golden, identity := buildServiceBundleFixture(t, root, "1.2.3", serviceFixtureSource)
	otherIdentity, ok := buildinfo.Expected("1.2.4", serviceFixtureSource, "darwin/"+runtime.GOARCH)
	if !ok {
		t.Fatal("invalid other identity")
	}
	other := filepath.Join(root, "other-factoryd")
	buildServiceBinary(t, other, "factoryd", otherIdentity)

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "symlink", mutate: func(t *testing.T, directory string) {
			path := filepath.Join(directory, "factoryd")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(directory, "factoryctl"), path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, directory string) {
			if err := os.Link(filepath.Join(directory, "factoryd"), filepath.Join(directory, "extra")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "writable mode", mutate: func(t *testing.T, directory string) {
			if err := os.Chmod(filepath.Join(directory, "factoryd"), 0o775); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "missing sibling", mutate: func(t *testing.T, directory string) {
			if err := os.Remove(filepath.Join(directory, "factory-runner")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mismatched identity", mutate: func(t *testing.T, directory string) {
			if err := copyServiceFile(other, filepath.Join(directory, "factoryd")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-"))
			copyServiceDirectory(t, golden, directory)
			test.mutate(t, directory)
			bundle, err := OpenServiceBundle(filepath.Join(directory, "factoryctl"), identity)
			if bundle != nil {
				_ = bundle.Close()
			}
			if !errors.Is(err, ErrServiceBundle) {
				t.Fatalf("mutation accepted: %v", err)
			}
		})
	}
	if bundle, err := OpenServiceBundle(filepath.Join(golden, "factoryctl"), buildinfo.Current()); bundle != nil || !errors.Is(err, ErrServiceBundle) {
		t.Fatalf("development expectation accepted: %v", err)
	}
	if bundle, err := OpenServiceBundle(filepath.Join(golden, "factoryd"), identity); bundle != nil || !errors.Is(err, ErrServiceBundle) {
		t.Fatalf("wrong entrypoint accepted: %v", err)
	}
}

func TestServiceBundleSourceReplacementFailsBeforeSnapshot(t *testing.T) {
	root := serviceTestRoot(t)
	source, identity := buildServiceBundleFixture(t, root, "1.2.3", serviceFixtureSource)
	bundle, err := OpenServiceBundle(filepath.Join(source, "factoryctl"), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	original := filepath.Join(source, "factoryd")
	moved := filepath.Join(source, "factoryd.opened")
	if err := os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	if err := copyServiceFile(filepath.Join(source, "factoryctl"), original); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(stage)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := bundle.Snapshot(parent, "factoryd"); !errors.Is(err, ErrServiceBundle) {
		t.Fatalf("source replacement snapshot = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(stage, "factoryd")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused snapshot left destination: %v", err)
	}
}

func TestServiceBundleSnapshotRequiresPrivateFreshStage(t *testing.T) {
	root := serviceTestRoot(t)
	source, identity := buildServiceBundleFixture(t, root, "1.2.3", serviceFixtureSource)
	bundle, err := OpenServiceBundle(filepath.Join(source, "factoryctl"), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	for _, mode := range []os.FileMode{0o755, 0o770} {
		directory := filepath.Join(root, "stage-"+mode.String())
		if err := os.Mkdir(directory, mode); err != nil {
			t.Fatal(err)
		}
		parent, err := os.Open(directory)
		if err != nil {
			t.Fatal(err)
		}
		err = bundle.Snapshot(parent, "factoryd")
		_ = parent.Close()
		if !errors.Is(err, ErrServiceBundle) {
			t.Fatalf("stage mode %o accepted: %v", mode, err)
		}
	}
	directory := filepath.Join(root, "occupied")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("do-not-replace")
	if err := os.WriteFile(filepath.Join(directory, "factoryd"), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	err = bundle.Snapshot(parent, "factoryd")
	_ = parent.Close()
	if err == nil {
		t.Fatal("occupied destination replaced")
	}
	got, readErr := os.ReadFile(filepath.Join(directory, "factoryd"))
	if readErr != nil || !bytes.Equal(got, sentinel) {
		t.Fatalf("occupied destination changed: %q, %v", got, readErr)
	}
}

func buildServiceBundleFixture(t *testing.T, root, version, source string) (string, buildinfo.Identity) {
	t.Helper()
	identity, ok := buildinfo.Expected(version, source, "darwin/"+runtime.GOARCH)
	if !ok {
		t.Fatal("invalid service fixture identity")
	}
	directory := filepath.Join(root, "bundle-"+version)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, component := range serviceBundleComponentNames() {
		buildServiceBinary(t, filepath.Join(directory, component), component, identity)
	}
	return directory, identity
}

func buildServiceBinary(t *testing.T, destination, component string, identity buildinfo.Identity) {
	t.Helper()
	command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -X github.com/dark-factory-build/dark-factory/internal/buildinfo.receipt="+identity.Receipt(), "-o", destination, "../../cmd/"+component)
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=darwin", "GOARCH="+runtime.GOARCH, "GOENV=off", "GOAUTH=off", "GOTOOLCHAIN=local")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", component, err, output)
	}
}

func copyServiceDirectory(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, component := range serviceBundleComponentNames() {
		if err := copyServiceFile(filepath.Join(source, component), filepath.Join(destination, component)); err != nil {
			t.Fatal(err)
		}
	}
}

func copyServiceFile(source, destination string) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.WriteFile(destination, body, 0o755)
}
