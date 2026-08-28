//go:build !darwin

package install

import (
	"context"
)

type launchctlRun func(context.Context, ...string) launchctlResult

type launchctlResult struct{}

func runLaunchctl(context.Context, ...string) launchctlResult { return launchctlResult{} }

func inspectServiceForAccount(context.Context, string, launchctlRun) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}

func inspectService(context.Context, string, string, launchctlRun) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}
