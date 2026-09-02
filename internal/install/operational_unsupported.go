//go:build !darwin

package install

import (
	"context"
	"os"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

type operationalHomeState struct{}
type localAPIState struct{}
type localAPIConnectionState struct{}
type localAPIDispatchState struct{}

func openOperationalHome(context.Context, string) (*OperationalHome, error) {
	return nil, ErrUnsupported
}

func (operationalHomeState) memberCapability(string) (MemberCapability, error) {
	return MemberCapability{}, ErrUnsupported
}

func (operationalHomeState) openStore(context.Context) (*kernel.Store, error) {
	return nil, ErrUnsupported
}

func (operationalHomeState) openLocalAPI(context.Context) (*LocalAPIAuthority, error) {
	return nil, ErrUnsupported
}

func (operationalHomeState) openCapability(string) (*os.File, error) {
	return nil, ErrUnsupported
}

func (operationalHomeState) close() error { return nil }

func (*localAPIState) verify() error                             { return ErrUnsupported }
func (*localAPIState) checkOperator([]byte) bool                 { return false }
func (*localAPIState) claimProtocol() (*LocalAPIProtocol, error) { return nil, ErrUnsupported }
func (*localAPIState) verifyProtocol(*LocalAPIProtocol) error    { return ErrUnsupported }
func (*localAPIState) checkProtocolOperator(*LocalAPIProtocol, []byte) bool {
	return false
}
func (*localAPIState) accept(*LocalAPIProtocol) (*LocalAPIConnection, error) {
	return nil, ErrUnsupported
}
func (*localAPIState) beginProtocolDispatch(*LocalAPIProtocol) (*LocalAPIDispatch, error) {
	return nil, ErrUnsupported
}
func (*localAPIState) close() error                     { return nil }
func (*LocalAPIConnection) Read([]byte) (int, error)    { return 0, ErrUnsupported }
func (*LocalAPIConnection) Write([]byte) (int, error)   { return 0, ErrUnsupported }
func (*LocalAPIConnection) SetDeadline(time.Time) error { return ErrUnsupported }
func (*LocalAPIConnection) PeerPID() (int, error)       { return 0, ErrUnsupported }
func (*LocalAPIConnection) CloseWrite() error           { return ErrUnsupported }
func (*LocalAPIConnection) Close() error                { return nil }
func (*LocalAPIDispatch) Close() error                  { return nil }
