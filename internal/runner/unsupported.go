//go:build !darwin

package runner

import (
	"context"
	"os"
	"time"
)

type GateLease struct{}
type OwnedChild struct{}

func PrepareExecSpec(ExecSpec) (*LaunchSpec, error) { return nil, ErrUnsupported }
func PrepareCommittedExecSpec(ExecutableCommitment, []string, []string, string) (*LaunchSpec, error) {
	return nil, ErrUnsupported
}
func CommitExecutableLocator(string) (ExecutableCommitment, error) {
	return ExecutableCommitment{}, ErrUnsupported
}
func (ExecutableCommitment) Verify() error { return ErrUnsupported }
func CreateGateLease(*os.File, *os.File, string) (*GateLease, FileIdentity, error) {
	return nil, FileIdentity{}, ErrUnsupported
}
func StartBlocked(*GateLease, string, *LaunchSpec, bool) (*OwnedChild, error) {
	return nil, ErrUnsupported
}
func (c *OwnedChild) Identity() Identity                          { return Identity{} }
func (c *OwnedChild) Activate() (FileIdentity, error)             { return FileIdentity{}, ErrUnsupported }
func (c *OwnedChild) Abort() error                                { return ErrUnsupported }
func (c *OwnedChild) Terminate(time.Duration) (Exit, error)       { return Exit{}, ErrUnsupported }
func (c *OwnedChild) FinishAfterExit(time.Duration) (Exit, error) { return Exit{}, ErrUnsupported }
func (c *OwnedChild) Close() error                                { return nil }
func ObserveProcess(Identity) Observation                         { return Observation{Presence: Unknown, Err: ErrUnsupported} }
func ObserveProcessGroup(Identity) Observation {
	return Observation{Presence: Unknown, Err: ErrUnsupported}
}
func RunExecGate() error      { return ErrUnsupported }
func RunAttemptRunner() error { return ErrUnsupported }

type AttemptController struct{}
type WorkerControl struct{}

func NewAttemptController() (*AttemptController, *os.File, error) {
	return nil, nil, ErrUnsupported
}
func (c *AttemptController) Configure(AttemptSpec) error { return ErrUnsupported }
func (c *AttemptController) Next(time.Duration) (AttemptEvent, error) {
	return AttemptEvent{}, ErrUnsupported
}
func (c *AttemptController) NextReady(time.Duration) (bool, error) { return false, ErrUnsupported }
func (c *AttemptController) Release(AttemptStage) error            { return ErrUnsupported }
func (c *AttemptController) Terminate() error                      { return ErrUnsupported }
func (c *AttemptController) Close() error                          { return nil }
func OpenWorkerControl() (*WorkerControl, error)                   { return nil, ErrUnsupported }
func (w *WorkerControl) Identity() Identity                        { return Identity{} }
func (w *WorkerControl) DuplicateRuntimeDirectory(context.Context) (*os.File, error) {
	return nil, ErrUnsupported
}
func (w *WorkerControl) ReportSelection([]byte) error   { return ErrUnsupported }
func (w *WorkerControl) AwaitPreparation() error        { return ErrUnsupported }
func (w *WorkerControl) ReportPreparation([]byte) error { return ErrUnsupported }
func (w *WorkerControl) AwaitPopulation() error         { return ErrUnsupported }
func (w *WorkerControl) ReportPopulation([]byte) error  { return ErrUnsupported }
func (w *WorkerControl) AwaitProvider() error           { return ErrUnsupported }
func (w *WorkerControl) ExecProvider(_ *LaunchSpec, cwd, task *os.File) error {
	if cwd != nil {
		_ = cwd.Close()
	}
	if task != nil {
		_ = task.Close()
	}
	return ErrUnsupported
}
func (w *WorkerControl) Close() error { return nil }
