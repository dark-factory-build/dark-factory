package api

import "context"

type outcomeCaller interface {
	call(context.Context, string, any, any) error
}

func writeOutcome(client outcomeCaller, ctx context.Context, method string, input OutcomeWriteInput) (Outcome, error) {
	var v Outcome
	err := client.call(ctx, method, input, &v)
	return v, err
}
func readOutcome(client outcomeCaller, ctx context.Context, method string, input OutcomeReadInput) (Outcome, error) {
	var v Outcome
	err := client.call(ctx, method, input, &v)
	return v, err
}
func listOutcome(client outcomeCaller, ctx context.Context, method string, input OutcomeListInput) (OutcomeList, error) {
	var v OutcomeList
	err := client.call(ctx, method, input, &v)
	return v, err
}

func (client *OperatorClient) OutcomeWrite(ctx context.Context, input OutcomeWriteInput) (Outcome, error) {
	return writeOutcome(client.client, ctx, "outcome_write", input)
}
func (client *OperatorClient) OutcomeRead(ctx context.Context, input OutcomeReadInput) (Outcome, error) {
	return readOutcome(client.client, ctx, "outcome_read", input)
}
func (client *OperatorClient) OutcomeList(ctx context.Context, input OutcomeListInput) (OutcomeList, error) {
	return listOutcome(client.client, ctx, "outcome_list", input)
}
func (client *AttemptClient) OutcomeWrite(ctx context.Context, input OutcomeWriteInput) (Outcome, error) {
	return writeOutcome(client.client, ctx, "attempt_outcome_write", input)
}
func (client *AttemptClient) OutcomeRead(ctx context.Context, input OutcomeReadInput) (Outcome, error) {
	return readOutcome(client.client, ctx, "attempt_outcome_read", input)
}
func (client *AttemptClient) OutcomeList(ctx context.Context, input OutcomeListInput) (OutcomeList, error) {
	return listOutcome(client.client, ctx, "attempt_outcome_list", input)
}

// Report reads factoryctl report or why; an attempt reader reads its own
// project's.
func (client *OperatorClient) Report(ctx context.Context, input ReportInput) (Report, error) {
	if !validReportInput(input) {
		return Report{}, ErrInvalidInput
	}
	var v Report
	err := client.client.call(ctx, "report", input, &v)
	return v, err
}

// Report reads why for the authenticated worker's own task.
func (client *AttemptClient) Report(ctx context.Context, input ReportInput) (Report, error) {
	if !validReportInput(input) {
		return Report{}, ErrInvalidInput
	}
	var v Report
	err := client.client.call(ctx, "attempt_report", input, &v)
	return v, err
}
