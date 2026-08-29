package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dark-factory-build/dark-factory/internal/cloudflareadmin"
)

const requiredWrapperReceipt = "dark-factory-reviewed-wrapper-v1"

var wrapperReceipt string

func main() {
	if wrapperReceipt != requiredWrapperReceipt {
		fmt.Fprintln(os.Stderr, "cloudflare-admin: invoke through scripts/with-cloudflare-env.sh")
		os.Exit(1)
	}
	repositoryRoot, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cloudflare-admin: resolve repository root: %v\n", err)
		os.Exit(1)
	}
	os.Clearenv()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := cloudflareadmin.Run(ctx, repositoryRoot, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "cloudflare-admin: %v\n", err)
		os.Exit(1)
	}
}
