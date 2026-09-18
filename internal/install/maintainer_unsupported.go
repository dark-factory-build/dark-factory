//go:build !darwin

package install

import "io"

func (*operationalHomeState) readMaintainerCredential() ([]byte, error) { return nil, ErrUnsupported }
func (*operationalHomeState) writeMaintainerCredential([]byte) error    { return ErrUnsupported }

func (*operationalHomeState) lockLegacyController() (io.Closer, error) { return nil, ErrUnsupported }
