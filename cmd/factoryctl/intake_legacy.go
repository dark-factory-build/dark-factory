package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

// Private structured transport for the packaged migration controller. Operators
// use `intake service migrate`; the controller never receives broker secrets.
func runLegacyIntakeProtocol(ctx context.Context, action string, input io.Reader, getenv func(string) string, output io.Writer) int {
	if getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE") != "" {
		return exitFailure
	}
	data, err := io.ReadAll(io.LimitReader(input, (256<<10)+1))
	if err != nil || len(data) > 256<<10 {
		return exitUsage
	}
	var request api.IntakeInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Action != action || !api.ValidIntakeInput(request) {
		return exitUsage
	}
	client, err := api.NewOperatorClient(getenv("DARK_FACTORY_SOCKET"), getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE"))
	if err != nil {
		return exitFailure
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	result, err := client.Intake(ctx, request)
	if err != nil || json.NewEncoder(output).Encode(result) != nil {
		return exitFailure
	}
	return 0
}
