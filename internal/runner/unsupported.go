//go:build !darwin

package runner

import (
	"os"
	"time"
)

type GateLease struct{}
type OwnedChild struct{}

func PrepareExecSpec(ExecSpec) (*LaunchSpec, error) { return nil, ErrUnsupported }
func CreateGateLease(*os.File, string) (*GateLease, FileIdentity, error) {
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
func RunExecGate() error { return ErrUnsupported }
