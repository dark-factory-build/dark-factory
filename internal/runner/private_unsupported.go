//go:build !darwin

package runner

import "os"

func validatePrivateDirectory(*os.File) (fileCommitment, error) {
	return fileCommitment{}, ErrUnsupported
}
