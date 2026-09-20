//go:build !darwin

package install

import "io"

func (*operationalHomeState) readCredential(string) ([]byte, error) { return nil, ErrUnsupported }
func (*operationalHomeState) writeCredential(string, []byte) error  { return ErrUnsupported }

func (*operationalHomeState) lockLegacyController() (io.Closer, error) { return nil, ErrUnsupported }
