package api

import "context"

func (client *OperatorClient) ContentCreate(ctx context.Context, input ContentInput) (Content, error) {
	var v Content
	err := client.client.call(ctx, "content_create", input, &v)
	return v, err
}
func (client *OperatorClient) ContentRevise(ctx context.Context, input ContentInput) (Content, error) {
	var v Content
	err := client.client.call(ctx, "content_revise", input, &v)
	return v, err
}
func (client *OperatorClient) ContentDeprecate(ctx context.Context, input ContentInput) (Content, error) {
	var v Content
	err := client.client.call(ctx, "content_deprecate", input, &v)
	return v, err
}
func (client *OperatorClient) ContentList(ctx context.Context, input ContentListInput) (ContentList, error) {
	var v ContentList
	err := client.client.call(ctx, "content_list", input, &v)
	return v, err
}
func (client *OperatorClient) ContentRead(ctx context.Context, input ContentReadInput) (Content, error) {
	var v Content
	err := client.client.call(ctx, "content_read", input, &v)
	return v, err
}
func (client *OperatorClient) ContentBody(ctx context.Context, input ContentBodyInput) (ContentBody, error) {
	var v ContentBody
	err := client.client.call(ctx, "content_body", input, &v)
	return v, err
}
func (client *OperatorClient) ContentEvidence(ctx context.Context, input ContentEvidenceInput) (ContentEvidence, error) {
	var v ContentEvidence
	err := client.client.call(ctx, "content_evidence", input, &v)
	return v, err
}
func (client *OperatorClient) ContentAttach(ctx context.Context, input ContentAttachInput) error {
	return client.client.call(ctx, "content_attach", input, &struct{}{})
}

func (client *AttemptClient) ContentCreate(ctx context.Context, input ContentInput) (Content, error) {
	var v Content
	err := client.client.call(ctx, "attempt_content_create", input, &v)
	return v, err
}
func (client *AttemptClient) ContentRevise(ctx context.Context, input ContentInput) (Content, error) {
	var v Content
	err := client.client.call(ctx, "attempt_content_revise", input, &v)
	return v, err
}
func (client *AttemptClient) ContentDeprecate(ctx context.Context, input ContentInput) (Content, error) {
	var v Content
	err := client.client.call(ctx, "attempt_content_deprecate", input, &v)
	return v, err
}
func (client *AttemptClient) ContentList(ctx context.Context, input ContentListInput) (ContentList, error) {
	var v ContentList
	err := client.client.call(ctx, "attempt_content_list", input, &v)
	return v, err
}
func (client *AttemptClient) ContentRead(ctx context.Context, input ContentReadInput) (Content, error) {
	var v Content
	err := client.client.call(ctx, "attempt_content_read", input, &v)
	return v, err
}
func (client *AttemptClient) ContentBody(ctx context.Context, input ContentBodyInput) (ContentBody, error) {
	var v ContentBody
	err := client.client.call(ctx, "attempt_content_body", input, &v)
	return v, err
}
func (client *AttemptClient) ContentEvidence(ctx context.Context, input ContentEvidenceInput) (ContentEvidence, error) {
	var v ContentEvidence
	err := client.client.call(ctx, "attempt_content_evidence", input, &v)
	return v, err
}
func (client *AttemptClient) ContentAttach(ctx context.Context, input ContentAttachInput) error {
	return client.client.call(ctx, "attempt_content_attach", input, &struct{}{})
}
