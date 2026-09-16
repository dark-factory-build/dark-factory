# Project content follow-up handoff

This follow-up is limited to the sent-back content slice. Existing SQLite
content-revision, evidence, and task-reference rows are the durable replay
identity: an exact repeated create, adjacent CAS revision, evidence, or task
attachment returns the existing row/no-op; a changed payload remains a
conflict. No schema or daemon-side operation path was added.

Changed paths: `internal/kernel/content.go` and this note. Body pages retain
byte offsets but reject offsets inside UTF-8 code points and never return a
partial code point; page ends retreat to a rune boundary. The public and
attempt callers continue to share the existing API dispatch and authority
paths.

Proof: `gofmt` completed; `go test -run '^$' ./internal/kernel ./internal/api
./internal/daemon ./cmd/factoryctl` compiled successfully; `go vet` reached
the existing duplicate JSON-tag findings in `internal/api/types.go`.
Behavioral tests were refused by the recovered runtime's path-authority
environment (`sqlite parent component "Users": operation not permitted`),
and Darwin supervisor fixtures also could not access Command Line Tools Git.
No live settings, credentials, provider, task, or deployment was changed.
