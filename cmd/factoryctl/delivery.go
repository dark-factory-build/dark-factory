package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func runDeliveryReconcile(ctx context.Context, getenv func(string) string, stdout, stderr io.Writer) int {
	return runDeliveryReconcileInput(ctx, os.Stdin, getenv, stdout, stderr)
}

func runDeliveryReconcileInput(ctx context.Context, input io.Reader, getenv func(string) string, stdout, stderr io.Writer) int {
	if getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE") != "" {
		return exitFailure
	}
	data, err := io.ReadAll(io.LimitReader(input, (256<<10)+1))
	if err != nil || len(data) > 256<<10 {
		return exitUsage
	}
	var request api.DeliveryInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return exitUsage
	}
	client, err := api.NewOperatorClient(getenv("DARK_FACTORY_SOCKET"), getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE"))
	if err != nil {
		return exitFailure
	}
	callContext, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	result, err := client.DeliveryReconcile(callContext, request)
	if err != nil {
		return writeWebFailure(stderr, "delivery reconcile", err)
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return exitFailure
	}
	return 0
}
