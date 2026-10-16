//go:build darwin

package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

type selfCheckFakeProcess struct{ failed string }

func (p *selfCheckFakeProcess) ReadCheckout(context.Context) error {
	if p.failed == "read checkout" {
		return errors.New("checkout unavailable")
	}
	return nil
}
func (p *selfCheckFakeProcess) CommitChange(context.Context) error {
	if p.failed == "commit Change" {
		return errors.New("commit refused")
	}
	return nil
}
func (p *selfCheckFakeProcess) ReachAttemptAPI(context.Context) error {
	if p.failed == "attempt API" {
		return errors.New("attempt API unavailable")
	}
	return nil
}
func (*selfCheckFakeProcess) Close() error { return nil }

type selfCheckFakeRunner struct{ failed string }

func (r selfCheckFakeRunner) Start(context.Context, Launch, string) (NativeSelfCheckProcess, error) {
	if r.failed == "start" {
		return nil, errors.New("provider did not start")
	}
	return &selfCheckFakeProcess{failed: r.failed}, nil
}

func TestRunNativeSelfCheckRequiresAllRealContractEdges(t *testing.T) {
	installation, runtime, _ := nativeFixture(t, kernel.ProviderCodex)
	request := requestFor(t, kernel.ProviderCodex, installation, runtime, "", "")
	for _, failed := range []string{"", "start", "read checkout", "commit Change", "attempt API"} {
		t.Run(failed, func(t *testing.T) {
			proof, err := RunNativeSelfCheck(context.Background(), request, t.TempDir(), selfCheckFakeRunner{failed: failed})
			if failed == "" {
				if err != nil || proof != (kernel.ProviderSelfCheck{Started: true, ReadCheckout: true, Committed: true, AttemptAPI: true}) {
					t.Fatalf("proof=%+v err=%v", proof, err)
				}
				return
			}
			if err == nil || proof.AttemptAPI && failed != "" {
				t.Fatalf("failed self-check proof=%+v err=%v", proof, err)
			}
		})
	}
}

func TestBoundedSelfCheckErrorTruncatesAtUTF8Boundary(t *testing.T) {
	err := boundedSelfCheckError("commit Change", errors.New(strings.Repeat("x", maxSelfCheckDiagnosticBytes-2)+"€"))
	if err == nil || !utf8.ValidString(err.Error()) || len(err.Error()) > maxSelfCheckDiagnosticBytes {
		t.Fatalf("diagnostic=%q valid=%v length=%d", err, err != nil && utf8.ValidString(err.Error()), len(err.Error()))
	}
}

func TestConfinedNativeSelfCheckRunnerUsesRealChangeAndAttemptEdges(t *testing.T) {
	checkout := t.TempDir()
	if output, err := exec.Command("/usr/bin/git", "-C", checkout, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	executable, err := runner.CommitExecutableLocator("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	launch := Launch{executable: executable, argv: []string{"/bin/sh", "-c", "self-check"}, environment: []string{"PATH=/usr/bin:/bin", "DARK_FACTORY_FACTORYCTL=/usr/bin/true"}, taskDelivery: TaskDeliveryAttemptAPI}
	process, err := (confinedNativeSelfCheckRunner{factoryctl: "/usr/bin/true"}).Start(context.Background(), launch, checkout)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	for name, check := range map[string]func(context.Context) error{
		"read checkout": process.ReadCheckout,
		"commit Change": process.CommitChange,
		"attempt API":   process.ReachAttemptAPI,
	} {
		if err := check(context.Background()); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestConfinedNativeSelfCheckUsesActualAttemptAPI(t *testing.T) {
	factoryctl := os.Getenv("DARK_FACTORY_FACTORYCTL")
	if factoryctl == "" {
		output, _ := exec.Command("/bin/sh", "-lc", "command -v factoryctl").Output()
		factoryctl = strings.TrimSpace(string(output))
	}
	if factoryctl == "" {
		t.Skip("factoryctl is not installed")
	}
	checkout := t.TempDir()
	if output, err := exec.Command("/usr/bin/git", "-C", checkout, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	executable, err := runner.CommitExecutableLocator("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	launch := Launch{executable: executable, argv: []string{"/bin/sh", "-c", "self-check"}, environment: []string{"PATH=/usr/bin:/bin", "DARK_FACTORY_FACTORYCTL=" + factoryctl, "DARK_FACTORY_SOCKET=/invalid"}, taskDelivery: TaskDeliveryAttemptAPI}
	process, err := (confinedNativeSelfCheckRunner{factoryctl: factoryctl}).Start(context.Background(), launch, checkout)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	if err := process.ReadCheckout(context.Background()); err == nil || !strings.Contains(err.Error(), "provider exited") {
		t.Fatalf("actual attempt API error=%v, want provider failure after invoking factoryctl", err)
	}
}
