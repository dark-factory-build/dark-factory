//go:build !darwin

package install

import (
	"context"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
)

type launchctlRun func(context.Context, ...string) launchctlResult

type launchctlResult struct{}

func runLaunchctl(context.Context, ...string) launchctlResult { return launchctlResult{} }

func inspectService(context.Context, string, string, launchctlRun) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}

type serviceBundleState struct{}

func openServiceBundle(string, buildinfo.Identity) (*ServiceBundle, error) {
	return nil, ErrUnsupported
}

func (*serviceBundleState) snapshot(*os.File, string) error { return ErrUnsupported }
func (*serviceBundleState) close() error                    { return nil }
