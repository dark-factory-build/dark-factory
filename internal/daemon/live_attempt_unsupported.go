//go:build !darwin

package daemon

import (
	"context"
)

const terminalEffectsSupported = false

func startLiveAttempt(*liveAttempt, context.Context) {}
