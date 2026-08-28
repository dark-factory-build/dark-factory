//go:build !darwin

package install

import "context"

func initHome(context.Context, string) (Result, error) { return Result{}, ErrUnsupported }

func inspectHome(context.Context, string) (Result, error) { return Result{}, ErrUnsupported }
