# Optional project library

Projects can keep procedures and acceptance scenarios as ordinary revisioned
Markdown. The existing local API, operator CLI and attempt CLI/MCP share one
SQLite copy. No provider startup, scheduler, verification stage, snapshot or
background lookup depends on the library. Procedures are guidance; selecting a
scenario does not execute it or make it a global gate.

List and read return metadata. `latest_revision` makes an old selection's
supersession visible. Body pages use UTF-8 byte offsets; follow `next_offset`
until `complete`, and retain the requested revision throughout. Small limits
that cannot fit the next complete code point are rejected. List/evidence pages
contain at most four records, including under worst-case JSON escaping. Bodies
are limited to 1 MiB in storage; the API also rejects requests whose encoded
JSON exceeds its 1 MiB frame. No input is silently truncated.

```text
factoryctl content create --id CONTENT_ID --project PROJECT_ID --kind procedure --title "Release checks" --body-file procedure.md
factoryctl content list --project PROJECT_ID --kind procedure
factoryctl content read --id CONTENT_ID --revision 1
factoryctl content body --id CONTENT_ID --revision 1 --offset 0 --limit 65536
factoryctl content attach --project PROJECT_ID --task TASK_ID --id CONTENT_ID --revision 1
factoryctl content attachments --project PROJECT_ID --task TASK_ID --revision TASK_WORK_REVISION
factoryctl content evidence --evidence-id EVIDENCE_ID --project PROJECT_ID --id SCENARIO_ID --revision 1 --tested-source COMMIT --environment "local macOS" --result passed --location EXISTING_REPORT_REFERENCE
factoryctl content evidence-list --project PROJECT_ID --id SCENARIO_ID --revision 1
```

The IDs above are nonzero 32-hex values. A supplied create/evidence ID permits
an exact retry; omitting it generates one. Revision/deprecation retries resolve
to the immutable revision originally inserted, even after later edits. A changed
payload at the same expected revision conflicts. Deprecation preserves history.
Task references pin both the content revision and the task work revision.

Use `attempt content ...` for live worker or overseer credentials. Workers can
contribute definitions and observations; only operators and live overseers can
attach selected definitions to work. Overseers attach to queued tasks in their
project. Authors/evaluators come from the authenticated operator or exact
run/agent/role, not caller labels. Observations remain separate from scenario
definitions and acceptance decisions, and reference existing results/reports.

The MCP catalogue lists the actual attempt commands. Its inline body input is
bounded; file/stdin input belongs to the ordinary CLI (`--body-file -` reads
stdin), since MCP runs outside the worker command sandbox. MCP refuses operator
commands and host file reads. All attempt reads and writes validate authority
and read/mutate within the same transaction.

The added schema objects are three optional tables and two indexes at schema
16. Prior schema fingerprints and migration steps remain unchanged. The v15
migration check preserves task prerequisites/conflict paths and starts the new
tables empty. Repeated command dispatch and write SQL are shared; the replaced
caller-supplied provenance inputs and unused evidence-by-ID entry point were
removed.

Focused behavioral coverage lives in `internal/kernel/content_test.go`,
`internal/daemon/content_api_test.go`, and the CLI/MCP tests. The unused-path
check in `internal/kernel/content_optional_test.go` compares ordinary admission
with zero and 32 unused library entries: the same admission result, task,
factory controls, events, snapshot, and SQLite statement count. Its benchmark
compares an ordinary admission poll with zero and 64 unused entries, without a
wall-clock pass/fail threshold. These tests do not claim real-provider adoption
or browser/deployment proof; those belong to the subsequent delivery slices.
