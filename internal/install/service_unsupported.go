//go:build !darwin

package install

import (
	"context"
)

type launchctlRun func(context.Context, ...string) launchctlResult

type launchctlResult struct{}

func runLaunchctl(context.Context, ...string) launchctlResult { return launchctlResult{} }

func inspectServiceForAccount(context.Context, string, ServiceConfig, launchctlRun) (ServiceStatus, error) {
	return ServiceStatus{}, ErrUnsupported
}

func inspectService(context.Context, string, string, launchctlRun) (ServiceStatus, error) {
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
