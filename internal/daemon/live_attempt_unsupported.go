//go:build !darwin

package daemon

import (
	"context"
)

func startLiveAttempt(*liveAttempt, context.Context) {}
