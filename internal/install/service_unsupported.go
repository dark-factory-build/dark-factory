//go:build !darwin

package install

import (
	"context"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
)

type launchctlRun func(context.Context, ...string) launchctlResult

type launchctlResult struct{}

func runLaunchctl(context.Context, ...string) launchctlResult { return launchctlResult{} }

func AccountHome() (string, error) { return "", ErrUnsupported }

func inspectServiceForAccount(context.Context, string, ServiceConfig, launchctlRun) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}

func serviceInstall(context.Context, string, ServiceConfig, string) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}

func serviceStart(context.Context, string, ServiceConfig) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}

func serviceStop(context.Context, string, ServiceConfig) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}

func serviceUninstall(context.Context, string, ServiceConfig) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}

func ServiceUpgrade(context.Context, string, string, buildinfo.Identity, int) error {
	return ErrUnsupported
}

func ServiceRollback(context.Context, string, bool, string) error { return ErrUnsupported }
