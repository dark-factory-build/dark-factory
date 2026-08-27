//go:build !darwin

package main

import (
	"context"
	"errors"
)

func openBrowser(context.Context, string) error {
	return errors.New("browser opening is unsupported on this platform")
}
