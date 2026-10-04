package api

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// ReleaseInput names one merged commit. Start begins its release; without it
// the call only reads the release's record.
type ReleaseInput struct {
	SHA   string `json:"sha"`
	Start bool   `json:"start,omitempty"`
}

func validReleaseInput(input ReleaseInput) bool {
	_, err := hex.DecodeString(input.SHA)
	return err == nil && len(input.SHA) == 40 && input.SHA == strings.ToLower(input.SHA)
}

func (client *OperatorClient) Release(ctx context.Context, input ReleaseInput) (kernel.ProductionDelivery, error) {
	var result kernel.ProductionDelivery
	if !validReleaseInput(input) {
		return result, ErrInvalidInput
	}
	if err := client.client.call(ctx, "release", input, &result); err != nil {
		return kernel.ProductionDelivery{}, err
	}
	return result, nil
}
