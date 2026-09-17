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

Bodies are ordinary files at an exact project repository, commit and path.
Browser and agent body writes create `.dark-factory/content/ID.md` in Git
without changing the worktree, index or `HEAD`. To adopt an existing file, use
`factoryctl content create` or `content revise` with `--commit OID --path PATH`
instead of `--body`; metadata reads return the pinned object format, commit and
path. A private create-only ref keeps the commit reachable after branch cleanup.
The stored repository identity rejects a replacement at the same project path.

SQLite retains authenticated actor, metadata, task attachments and evaluation;
the Git commit author identifies only the generated Git object. Homes upgraded
from the earlier SQLite-body format export each body on its first authenticated
read and clear the blob only after the Git ref and SQL pin both succeed. If the
project repository is temporarily unavailable, that authenticated read still
serves the stored legacy body and retries export later.

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
