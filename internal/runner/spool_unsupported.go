//go:build !darwin

package runner

func publishNoReplace(int, string, string) error { return ErrUnsupported }
