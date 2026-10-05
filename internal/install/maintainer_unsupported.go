//go:build !darwin

package install

func (*operationalHomeState) readCredential(string) ([]byte, error) { return nil, ErrUnsupported }
func (*operationalHomeState) writeCredential(string, []byte) error  { return ErrUnsupported }
