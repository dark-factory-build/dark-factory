//go:build darwin || linux

package api

import (
	"context"
)

func (client *OperatorClient) DeliveryReconcile(ctx context.Context, input DeliveryInput) (DeliveryResult, error) {
	if !validDeliveryInput(input) {
		return DeliveryResult{}, ErrInvalidInput
	}
	var result DeliveryResult
	if err := client.client.call(ctx, "delivery_reconcile", input, &result); err != nil {
		return DeliveryResult{}, err
	}
	if result.TaskIDs == nil {
		result.TaskIDs = []string{}
	}
	for _, id := range result.TaskIDs {
		if !validID(id) {
			return DeliveryResult{}, ErrProtocol
		}
	}
	return result, nil
}
