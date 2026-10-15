//go:build darwin

package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
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
