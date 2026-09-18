//go:build !darwin

package install

func (*operationalHomeState) readMaintainerCredential() ([]byte, error) { return nil, ErrUnsupported }
func (*operationalHomeState) writeMaintainerCredential([]byte) error    { return ErrUnsupported }
