// Package install owns the private, stopped Go home bootstrap contract.
package install

import (
	"context"
	"errors"
)

var (
	ErrUnsupported = errors.New("home initialization is unsupported on this platform")
	ErrInvalidHome = errors.New("home path is invalid")
	ErrUncertain   = errors.New("home publication outcome is uncertain")
)

type State uint8

const (
	Published State = iota + 1
	Ready
)

// Result describes a successful operation without exposing private contents.
type Result struct {
	State State
}

// Init creates and atomically publishes one fresh Go home, or reports that an
// exact home is already ready. It never repairs an existing path.
func Init(ctx context.Context, home string) (Result, error) { return initHome(ctx, home) }

// Doctor validates one stopped fresh Go home without modifying the filesystem.
func Doctor(ctx context.Context, home string) (Result, error) { return inspectHome(ctx, home) }
