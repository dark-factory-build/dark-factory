# Optional procedures, scenarios and outcomes

The project library is voluntary. Ordinary tasks remain ordinary tasks: no
library lookup, extra model turn, scheduler rule or acceptance check is added
to an unused task. Do not change an overseer's standing instruction merely to
adopt these features. Read [the library operations](PROJECT_CONTENT_HANDOFF.md)
for bounded CLI/API/MCP access and exact revision attachments.

A procedure should state its purpose, applicable conditions, steps and known
limits. An `acceptance_scenario` should state the question, inputs, environment,
actions and observable expected result. Its definition is not observed evidence.
Select only relevant revisions; a worker can explicitly read its task's attached
references, then fetch the bodies it needs. Corrections append a revision rather
than changing what earlier work used.

After running a chosen scenario, record an observation with the exact definition
revision, tested source, environment, result, evaluator and existing report/run
reference. Keep large reports in their existing location. Comparing observations
from different sources or environments is a judgment, not an automatic pass.

## Outcomes over existing work

`factoryctl outcome write --project PROJECT --id OUTCOME --document-file file.json`
creates an immutable revision. Add `--revision CURRENT_REVISION` to revise it.
`outcome read --project PROJECT --id OUTCOME --revision REVISION` reads history;
omit the revision for current metadata and document. `outcome list --project
PROJECT` pages metadata. The attempt CLI uses `attempt outcome`; MCP advertises
inline `--document` input and refuses host file/stdin reads. Encoded documents
are bounded to 32 KiB; no partial write or truncation is allowed.

A minimal document is:

```json
{
  "kind": "outcome",
  "objective": "Make the selected release usable",
  "criteria": "Required checks pass and the installed identity is verified",
  "source_issue": "https://github.com/OWNER/REPOSITORY/issues/123",
  "state": "open"
}
```

Alternatively anchor to an existing `anchor_task_id` and exact
`anchor_work_revision`. Link existing implementation, investigation, review and
delivery tasks in `links`; a stage of `implemented`, `reviewed`, `merged` or
`deployed` requires cited evidence. Task success alone never means accepted.

A worker can propose an outcome for its admitted task/work revision. It cannot
change the objective of an existing outcome or record independent acceptance.
An overseer or human can accept explicit criteria with a `conclusion`, `reason` and
existing evidence IDs, or a visibly labelled `authorized judgment:`. Record
`remaining_work` separately. Reopen when the objective or criteria change.
Edits to a task's actual objective make older acceptance stale; history remains
readable and missing references are explicit. Source issue text is not polled
or synchronized. An outcome write never edits or schedules its linked tasks.

## Compare existing candidates

Use `kind: comparison`, a question and criteria, one baseline and existing
candidate task/run references. Each candidate records exact source/environment,
scenario evidence and whether the evidence is `measurement` or `subjective`.
Record `select` with a selected candidate task ID, `keep_baseline`, or
`inconclusive`, with the reason and evaluator. Preserve rejected candidates and
their results. Rejection of a design does not turn successful investigation into
a failed worker task. Comparing does not rerun work, create branches or bypass
review, merge or deployment. Capacity one supports sequential candidate tasks;
parallel work uses ordinary capacity controls.

`kind: mission` supports a manually maintained objective. A bounded milestone
may name `milestone_of`; accepting it does not complete the parent mission.
There is no campaign scheduler or autonomous optimization loop.

## Browser use

The existing console sidebar has a collapsed **Project library & outcomes**
panel. Opening it or leaving it unused causes no fetch. Browse metadata, select
an exact revision, then explicitly read body or evidence pages. Authoring saves
new revisions; superseded definitions and existing attachments remain unchanged.
Attach a selected revision to queued work or open an ordinary agent instruction
draft for review and explicit submission. An outcome links existing work; saving
or accepting it does not enqueue, replay or publish anything.

Private-detail permission is required for reads; private-detail plus human-actions
permission is required for authoring. Every resource operation validates the
selected project. The protocol uses existing pairing, revocation gates and
request correlation; there is no new credential or capability bit. Metadata
pages contain one record and body pages at most 8 KiB. Responses exceeding the
browser frame or exact JSON integer limit return `too_large` without closing
the session; use CLI/API for such records. Stored revisions remain unrestricted
by that browser transport limit. The browser never automatically retries writes
or replays task instructions after reconnecting.
