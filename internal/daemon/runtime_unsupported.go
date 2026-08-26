//go:build !darwin

package daemon

import (
	"context"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/runner"
)

type Runtime struct{}

type PrivateFile struct{}

func (PrivateFile) Path() (string, error)         { return "", errUnsupported }
func (PrivateFile) Identity() runner.FileIdentity { return runner.FileIdentity{} }
func (PrivateFile) String() string                { return "private runtime file" }
func (PrivateFile) GoString() string              { return "daemon.PrivateFile{private}" }

func CreateRuntime(*os.File, string) (*Runtime, error) { return nil, errUnsupported }
func (*Runtime) Path() (string, error)                 { return "", errUnsupported }
func (*Runtime) Identity() runner.FileIdentity         { return runner.FileIdentity{} }
func (*Runtime) DuplicateDirectory() (*os.File, error) { return nil, errUnsupported }
func (*Runtime) PublishAttemptToken(context.Context, [32]byte) (PrivateFile, error) {
	return PrivateFile{}, errUnsupported
}
func (*Runtime) PublishWorkerConfig(context.Context, workerConfig) (PrivateFile, error) {
	return PrivateFile{}, errUnsupported
}
func (*Runtime) Close() error { return nil }
