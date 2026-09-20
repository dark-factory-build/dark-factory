package api

import "context"

func (client *OperatorClient) ProductionObserve(ctx context.Context, input ProductionInput) (ProductionResult, error) {
	if !validProductionInput(input) {
		return ProductionResult{}, ErrInvalidInput
	}
	var result ProductionResult
	if err := client.client.call(ctx, "production_observe", input, &result); err != nil {
		return ProductionResult{}, err
	}
	if result.State == "" || !validText(result.State, 1, 128) {
		return ProductionResult{}, ErrProtocol
	}
	return result, nil
}
