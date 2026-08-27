//go:build darwin

package main

import (
	"context"
	"os/exec"
)

func openBrowser(ctx context.Context, value string) error {
	return exec.CommandContext(ctx, "open", value).Run()
}
