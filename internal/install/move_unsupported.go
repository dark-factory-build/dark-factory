//go:build !darwin

package install

import "context"

func moveHome(context.Context, string, string) error { return ErrUnsupported }
