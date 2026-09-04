//go:build !darwin

package daemon

import (
	"context"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

type Runtime struct{}

type RecoveredRuntime struct{}

type RuntimeBinding struct{}

type RuntimeParent struct{}

type RuntimeLeasePresence uint8

const (
	RuntimeLeaseHeld RuntimeLeasePresence = iota + 1
	RuntimeLeaseAvailable
)

type PrivateFile struct{}

func (PrivateFile) Path() (string, error)         { return "", errUnsupported }
func (PrivateFile) Identity() runner.FileIdentity { return runner.FileIdentity{} }
func (PrivateFile) String() string                { return "private runtime file" }
func (PrivateFile) GoString() string              { return "daemon.PrivateFile{private}" }

func OpenRuntimeParent(context.Context, install.MemberCapability, string) (*RuntimeParent, error) {
	return nil, errUnsupported
}
func (*RuntimeParent) Close() error { return nil }
func CreateRuntime(*RuntimeParent, string) (*Runtime, error) {
	return nil, errUnsupported
}
func AdoptRuntime(*RuntimeParent, string) (*Runtime, error) { return nil, errUnsupported }
func OpenRecoveredRuntime(context.Context, *RuntimeParent, string, runner.FileIdentity) (*RecoveredRuntime, error) {
	return nil, errUnsupported
}
func (*RecoveredRuntime) AcknowledgeTerminal(*runner.TerminalRecord, kernel.Run, kernel.Resource, kernel.Resource, kernel.Resource) error {
	return errUnsupported
}
func (*RecoveredRuntime) Close() error             { return nil }
func (*Runtime) Binding() (*RuntimeBinding, error) { return nil, errUnsupported }
func (*RuntimeBinding) Values() (string, runner.FileIdentity, error) {
	return "", runner.FileIdentity{}, errUnsupported
}
func (*RuntimeBinding) ProviderHome() (string, error)     { return "", errUnsupported }
func (*RuntimeBinding) ProviderTemp() (string, error)     { return "", errUnsupported }
func (*RuntimeBinding) AttemptTokenPath() (string, error) { return "", errUnsupported }
func (*Runtime) DuplicateRunnerFiles() (*os.File, *os.File, error) {
	return nil, nil, errUnsupported
}
func ObserveRuntimeLifetime(*RuntimeParent, string, runner.FileIdentity) (RuntimeLeasePresence, error) {
	return 0, errUnsupported
}
func RemoveRecordedRuntime(context.Context, *RuntimeParent, string, runner.FileIdentity) (bool, error) {
	return false, errUnsupported
}
func (*Runtime) PublishAttemptToken(context.Context, [32]byte) (PrivateFile, error) {
	return PrivateFile{}, errUnsupported
}
func (*Runtime) Close() error { return nil }
