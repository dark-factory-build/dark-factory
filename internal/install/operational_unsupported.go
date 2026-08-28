//go:build !darwin

package install

import (
	"context"
	"net"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

type operationalHomeState struct{}
type localAPIState struct{}

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

func (*localAPIState) verify() error                         { return ErrUnsupported }
func (*localAPIState) checkOperator([]byte) bool             { return false }
func (*localAPIState) claimProtocol() error                  { return ErrUnsupported }
func (*localAPIState) accept() (*net.UnixConn, error)        { return nil, ErrUnsupported }
func (*localAPIState) releaseConnection(*net.UnixConn) error { return nil }
func (*localAPIState) close() error                          { return nil }
