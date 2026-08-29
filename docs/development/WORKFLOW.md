# Development workflow

## Live-use boundary

Live use remains frozen until an independent exact-main boot review passes.
Do not start provider work, install or release a development revision, enable
dispatch, modify `~/.dark-factory`, load or alter the installed launchd job,
or delete retained Changes. Isolated fixtures use a temporary home, explicit
socket, exact resource identities, and an independent reaper.

## Day to day

1. Create one branch in one development worktree. For agent automation, first
   obtain a current local base through the remote-access boundary below; the
   helper itself never contacts a remote. Human contributors may refresh their
   checkout through their normal Git workflow.

   ```sh
   ./scripts/new-worktree.sh <slug>
   cd .worktrees/<slug>
   ```

2. Make one coherent change. Preserve unrelated dirty work and prefer deletion
   over compatibility machinery.
3. Run focused checks through the shared lease when they invoke
   process-sensitive fixtures:

   ```sh
   ./scripts/with-local-ci-lease.sh go test ./internal/daemon/
   ```

4. Run the authoritative gate on the exact head:

   ```sh
   ./scripts/local-ci.sh
   ```

   The gate takes no arguments and runs on macOS only: the daemon is
   Darwin-only, and Linux support is #120/#141-144.
5. Publish the branch and open a PR through the remote-access boundary below,
   describing behavior, deleted authority paths, exact base/head, focused
   proof, and unverified lanes.
6. A reviewer other than the author reads `base..head` cold, tries to break
   correctness/security/simplification, and posts findings plus what resisted
   attack. The author resolves every finding; the reviewer rechecks and gives
   an explicit ALLOW before merge.
7. Required hosted checks must pass on the exact reviewed head. Merge only
   then. Remove the development worktree after merge through the normal Git
   worktree command; never remove preserved factory Changes during this work.

### Shared local-CI lease

The macOS gate serializes compiler, release-probe, and process-sensitive work
across linked worktrees using a repository-common-directory lease. Its owner
record is diagnostic; the held kernel lock is authoritative. Do not bypass the
wrapper for a load-bearing process fixture. Set
`DARK_FACTORY_LOCAL_CI_WAIT=0` to refuse instead of waiting.

The gate clears inherited live-factory home, socket, and attempt identity
variables, and provisions its own `GOCACHE` and `GOMODCACHE` under an isolated
stage root. Tests set their own isolated values.

The Rust-policy completion lane described in
[SECURITY.md](../../SECURITY.md) is a product capability for verifying Rust
projects the factory works on; it is not exercised by any gate stage today,
because the supervisor admits only the `none` verification policy.

## Isolated daemon checks

Use a second, throwaway home and explicit socket. Never rely on the default:

```sh
root="$(mktemp -d /private/tmp/df-dev.XXXXXX)"; chmod 700 "$root"
# factoryd locates its runner and factoryctl as siblings of its own
# executable, so build all three into one directory; `go run` cannot satisfy
# that and fails with "runtime unavailable".
go build -o "$root/factoryd" ./cmd/factoryd
go build -o "$root/factoryctl" ./cmd/factoryctl
go build -o "$root/factory-runner" ./cmd/factory-runner

# `init` creates the home, so hand it a path that does not exist yet.
home="$root/factory"
"$root/factoryctl" init --home "$home"
"$root/factoryctl" doctor --home "$home"   # doctor validates a *stopped* home
"$root/factoryd" --home "$home" &

# Wait for the daemon to bind; the operator client refuses a missing socket.
until [ -S "$home/runtimes/factory.sock" ]; do sleep 0.2; done

export DARK_FACTORY_SOCKET="$home/runtimes/factory.sock"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$home/operator.token"
"$root/factoryctl" project create --name dev --root "$PWD"
```

Three constraints that block the obvious shorter version: the root must be
under `/private/tmp`, because `/tmp` is a symlink and the home walk opens every
component with `O_NOFOLLOW`; `doctor` reports a running daemon's home as not an
exact stopped home, so run it before starting `factoryd`; and every operator
command needs both environment variables above, not the socket alone.

Worker lifecycle checks must use the deterministic shell provider and a tiny
temporary Git repository. They must prove the provider receives one
daemon-owned `.git`-free Change and that the same run and source survive the
injected boundary. Do not point any fixture at a real provider.

A lifecycle fixture must register resources before use and verify after its
test that exact descendants and disposable paths are gone. Crash/restart tests
must restart the daemon and let its durable finalizer converge. `Drop`, shell
traps, sleeps, broad process scans, and cleanup owned only by the killed fixture
are insufficient proof.

The macOS contributor smoke and the opt-in launchd release proof that used to
cover these cuts were deleted with the Rust workspace: both drove
`target/debug` binaries that no build produces. Their durable-receipt ledger
(`DARK_FACTORY_LAUNCHD_GATE_LEDGER`) went with them; nothing writes it now, so
there is no ledger to resume or recover.

Release replacement and rollback against a real launchd job is the one lane
that lost coverage in that deletion and has no Go successor yet — it is
recorded in GO_REWRITE.md as follow-up, not offered here as something an
operator can run.

Containment is proved by `internal/install`'s service tests against a
recorded `launchctl`, and end to end by `scripts/go-service-e2e.sh`, which
drives a real disposable launchd label through install, start, stop, start and
uninstall. Hosted macOS runners get a fresh `TMPDIR` per job, so cross-run
resume is exercised by those tests and by local dogfooding rather than by CI.

## Review discipline

Each PR is one coherent change. Before review, record:

- the old authority paths deleted;
- production and test additions/deletions separately;
- exact causal tests and injected crash boundaries;
- unsupported operations that fail closed;
- migration preconditions and rollback requirements; and
- any compatibility code retained, with its sole caller.

Security-sensitive changes receive an independent adversarial review against
[`SAFE_KERNEL_REFACTOR.md`](SAFE_KERNEL_REFACTOR.md). Merge, boot approval,
release, installation, and live verification remain separate decisions.

## Migration rules

The Go kernel has one fresh schema in `internal/kernel/schema.go` and
deliberately no migration chain, upcaster, or compatibility layer: the Go home
and schema are new. A schema change edits that set together with the causal
tests that pin it.

The rules below describe the retired Rust kernel's cutover migrations and are
kept as the record of what a future migration boundary must honour if one is
ever introduced. Kernel cutover migrations refuse databases containing live legacy authority or
other external effects whose completion cannot be proven. Preserved source
paths migrate into metadata-only `legacy_sources` quarantine, including
separate records when agents or projects shared a path. Factoryd never inspects
or owns those paths; forgetting a record does not touch the filesystem. Before
an operator database crosses an irreversible migration boundary, take an
explicit backup and rollback decision. Migration never implies boot approval.

## CI and GitHub

The pull-request workflow runs the Darwin-only Go runtime gate on hosted
macOS, the separate maintainer control-plane gate on hosted Ubuntu, and the
exact-head review check in the merge queue. Their aggregate `required` context
is the merge gate; there is no Linux runtime lane yet.

Merges go through a **merge queue**, not straight to `main`. An approved,
green pull request is enqueued; GitHub then builds `main` + every entry ahead
of it + this one on a temporary `gh-readonly-queue/**` ref, runs the same
`required` gate against that exact combination via a `merge_group` event, and
merges only if it passes — ejecting the entry if it does not. Entries build
speculatively in parallel, so a queue of branches costs roughly one CI run,
not one per branch.

This replaces "require branches to be up to date before merging", which is now
off. That setting guaranteed the same property by forcing every open branch to
be rebased and fully re-tested after each merge, which serialises a queue of
`n` branches into `n` CI runs and dismisses each approval on the way. The queue
establishes the same thing once. `required` still gates, history is still
linear, and force-push and deletion are still refused.

Owner approval is scoped by path rather than demanded of every pull request.
`main-review` sets `required_approving_review_count` to 0 and keeps
`require_code_owner_review`, so `.github/CODEOWNERS` decides. Owned paths are
the authority surface: `.github/`, the agent rule and boundary documents
(`AGENTS.md`, `CLAUDE.md`, `SECURITY.md`, `ARCHITECTURE.md`), and the four
named scripts that publish the ruleset, verify the recorded review verdict,
or causally test those two. A change touching any of them stops for one owner
approval, re-earned after every push because stale reviews are dismissed.
Everything else -- the Go runtime, `control-plane/`, the rest of `scripts/`, and
docs -- merges on the `required` aggregate in the queue, whose `review` check
demands an adversarial-review verdict at the exact head, with no human
approval at any point.

That is not a relaxation of review so much as a repair of it. The owner
authors most pull requests here and GitHub never lets an author approve their
own, so a blanket count of 1 meant every owner-authored change merged by admin
bypass — the gate satisfied by circumventing it rather than by meeting it.
Scoping by path means the approvals that do happen are real ones.

Rule 2's adversarial review is enforced separately, by the `review` check. The
review itself happens in the factory, where an
agent that did not write the change reads the diff; what GitHub sees is the
verdict the reviewer records through the maintainer App:

```sh
submit_pull_request_review  event: ALLOW  head_sha: <the exact commit reviewed>
```

`scripts/verify-adversarial-review.sh` reads the pull request's reviews and
passes only when the App recorded an `ALLOW` against exactly the head being
merged, with no blocking verdict at that head. The reviewer's findings are
written to the run summary, so a red check says what was wrong rather than
only that something was.

A blocking verdict is recorded the same way, with `event: REQUEST_CHANGES`.
All three verdicts reach GitHub as a `COMMENT` review — the App authors the
pull requests it reviews, and GitHub refuses a self-review that takes a side,
`APPROVE` and `REQUEST_CHANGES` alike — so what distinguishes them is the line
the App writes, and that line is what the check reads to tell them apart. It is
not the only thing the check reads: a review it considers that carries GitHub's
own `CHANGES_REQUESTED` state blocks on the state alone, ahead of any line. No
verdict this App submits can produce that state any more, and the branch is
kept anyway — a backstop the merge gate should not have to assume away.

`review` is part of the `required` aggregate, which is what makes rule 2 a
merge condition rather than a convention:

```yaml
needs: [checks, control-plane, review]
# In the queue only `success` passes. Elsewhere `skipped` passes too, since
# `review` runs on merge_group alone and there is no verdict to read. Treating
# that as a failure would block every pull request.
if: needs.review.result != 'success' && (github.event_name == 'merge_group' || needs.review.result != 'skipped')
```

Keyed on the event as well as the result on purpose. Keying on the result alone
is sound only while `review` has no `needs:` and so cannot be
dependency-skipped inside the queue — a property of a different job that a
reader would have to go and check. This form states the policy directly.

It arrived in two changes rather than one, because a single pull request adding
the gate and requiring it would have deadlocked: the job runs the default
branch's copy of a script that would not have existed there yet. The same
ordering applied to the control plane — until `ALLOW` was deployed, requiring a
verdict would have demanded something nothing could produce.

`scripts/test-repository-settings.sh` extracts both the step's condition and
its body from the `required` job and checks them, rather than grepping the file
for the strings. Grepping is not enough, and each weaker form was defeated by a
mutation rather than by argument: a pinned string is satisfied by a comment
above a step whose real `if:` is `false`; a substring test for `exit 1` is
satisfied by `# exit 1`; an extraction bounded only by the step name reads a
decoy step of the same name in a job that never runs. Every one leaves a gate
that runs, prints its diagnostic, and passes.

The `merge_queue` rule is therefore a single chokepoint, and the `required` job
asserts it on every run against the **live** rules — not against
`scripts/github-repo-settings.sh`, which records what an operator intended to
apply rather than what is applied, and has already drifted from it once. Five
facts are required: the `merge_queue` rule, its `ALLGREEN` grouping, the
`required_status_checks` rule, the `required` context within it, and that
context's binding to GitHub Actions (integration `15368`). If any lapses,
`merge_group` stops firing or stops gating, the review gate never runs or
becomes reportable by an integration that never ran it, and `required` would
otherwise stay green while rule 2 quietly stopped being enforced. The binding
fact names its context — `integration:15368:required`, not a bare
`integration:15368` — so a second required context carrying the binding cannot
stand in for `required` losing it. Rulesets in `evaluate` or `disabled`
enforcement are not returned by that endpoint, so a ruleset downgraded out of
enforcement reads as absent — which is the answer we want.

That assertion is written **inline** in `.github/workflows/ci.yml` rather than
in a script under `scripts/`, and it is the one place in this repository where
inline is the safer arrangement. A helper under `scripts/` is publishable by
the maintainer App, so it would be the weaker-protected file guarding the
stronger-protected one — the exact reason `verify-adversarial-review.sh` is
hoisted to the default branch instead. Hoisting cannot work here: a
default-branch copy does not exist until the change has already merged, and
this assertion has to hold from its first run.

`scripts/test-inline-chokepoint.sh` extracts that step's script from `ci.yml`
verbatim and runs it against fixtures with a stubbed `gh`, so the shipped
assertion is exercised byte-for-byte rather than a copy that could diverge from
it. The test itself is App-publishable and that is fine: neutering it weakens
only the test, never the assertion, which sits where the App cannot write.

One cost is worth naming. `required` — the single required context — now
depends on a live GitHub API call on every event, so a rules-API outage turns
every merge red. That is the correct direction to fail, but it is new
availability coupling on the one context the ruleset requires.

What the check does **not** do is judge the review. A reviewer that records an
`ALLOW` without reading anything produces a green check, and the reviewing and
authoring agents reach the App through the same credential — so this is
evidence that a review was recorded, not proof one was earned. It closes the
gap where a real review happened and GitHub could not see it.

Three properties are worth stating precisely, because each replaces something
an approval used to do:

- **Exact-head binding.** GitHub records `commit_id` on the review from the
  App's request, and the App also renders the head into a `Dark-Factory-Review:`
  line it writes itself — a review body carrying that prefix is refused, so a
  caller cannot state a verdict it did not ask for. Both must name the head
  under test. Pushing a fix orphans every prior verdict, which is what
  `dismiss_stale_reviews_on_push` did for human approvals.
- **The decision is not part of the change it gates.** The `review` job runs
  the copy of `verify-adversarial-review.sh` on the default branch, never the
  one in the pull request. `scripts/` is a path the maintainer App *can*
  publish to, so without this an agent-authored pull request could weaken the
  reviewer about to judge it. The *plumbing* is not hoisted: the projection,
  the head lookup, and whether the job runs at all still come from the pull
  request's own `ci.yml`, bounded by CODEOWNERS on `.github/` and by the App's
  refusal to publish under `.github/workflows/`. That refusal is by named
  path — `.github/actions/**` is publishable — so a composite action extracted
  from this job would leave the gate's plumbing inside App reach.
- **It runs only in the merge queue.** Recording a verdict fires no workflow
  event, so a pull-request-time run would evaluate before the reviewer acted,
  fail, and have no way back to green: re-triggering means pushing, which moves
  the head and orphans the verdict. Requiring it there would make every pull
  request permanently unmergeable and the release valve would be an admin
  bypass. The queue runs after the verdict exists, against the head being
  merged. A pull request with no verdict enqueues and is ejected.
- **Every entry is checked, and that depends on a ruleset setting.** Each queue
  entry gets its own `merge_group` run whose ref names exactly one pull request
  (`gh-readonly-queue/<base>/pr-<n>-<base sha>`), so entries are verified
  against their own heads rather than the group's synthetic head commit. This
  holds because `main-protect` sets `grouping_strategy: ALLGREEN`, where every
  entry's own group must pass. Under `HEADGREEN` only the last entry's group
  must, and unreviewed entries ahead of it would merge unchecked — so that
  setting in `scripts/github-repo-settings.sh` is load-bearing for this gate,
  not just for CI cost.

Review the exact `.github/workflows/` diff before approving an external run: a
PR evaluates its own workflow and can change `runs-on`. `.github/` is an owned
path in CODEOWNERS for exactly this reason, and the maintainer App refuses to
publish under `.github/workflows/` at all. A green workflow never replaces
review and resolved review threads.

Agent automation must use the provider-neutral Dark Factory Maintainer MCP at
`https://maintainer.darkfactory.build/mcp` for every authenticated remote
operation. The host or coordinator supplies its transport authentication; it
is not a Codex, Claude, or other model-provider connector. Before any remote
operation, call `maintainer_status` and require the exact repository
`dark-factory-build/dark-factory`, numeric repository ID `1335380107`, and
permission revision `maintainer-operations-v2`.

Host registration must isolate the Cloudflare Access pair inside the MCP
transport process. Do not export either value into a coordinator-wide,
provider, tool, or shell environment. An MCP-compatible client may use a local
stdio-to-HTTPS transport for that isolation; this is a generic MCP boundary,
not authority granted by a particular model provider or client configuration.

The deployed surface supports authority, default-head, and durable-operation
observation; bounded issue lifecycle; exact commit and pull-request publication;
exact-head review, workflow diagnosis/recovery, and eventual-merge observation;
merge-queue enqueue; immutable release publication/observation/recovery; and an
exact dispatch of the reviewed control-plane deployment workflow. GitHub's
required `delete_branch_on_merge` repository setting owns source-branch cleanup
atomically. It does not expose generic remote reads, direct merge, arbitrary ref
mutation, arbitrary issue mutation, or a generic Actions/API proxy. An operation
outside that set is an explicit human handoff; it is not a reason to borrow
operator credentials.

Retain the canonical request for every write until it completes. If the MCP
transport closes without a result, call `observe_operation` with the same UUID.
A completed record returns the typed result; a missing record proves the
request never reached the journal; planned, executing, and indeterminate states
remain bound to that request. Retry only the byte-identical request under the
same UUID. If local assembly was truncated or otherwise wrong, do not reuse its
UUID for corrected bytes.

Agents must not use ambient `git fetch`, `git pull`, `git clone`, `git push`,
`gh`, `gh auth`, or SSH-based access: those paths can consult the operator's
credential helper, login keychain, SSH agent, or other user credential state.

If no authorized surface is available, remote access stops without contacting
GitHub. There is no token fallback for agent operations: operator approval does
not authorize injecting `GH_TOKEN`, `GITHUB_TOKEN`, a personal access token,
or another credential into an agent process. Supported operations go through
the Maintainer MCP; unsupported operations require a separately reviewed human
action.

### Local Cloudflare API credentials

Cloudflare authentication is separate from GitHub authority. For an explicitly
owner-authorized DNS operation, use the ignored `.env.txt` at the common
worktree root through the repository helper:

```sh
./scripts/with-cloudflare-env.sh dns status
# Only with separate authorization to create this one record:
./scripts/with-cloudflare-env.sh dns publish-app
```

The file must be a regular mode-`0600` file containing exactly one usable
`CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID`. The public launcher replaces
itself with an explicit empty environment before starting any setup child. The
compiled helper does not source the file: it opens it atomically without
following a symlink and extracts only those two assignments. It first captures
one exact Git commit, refuses mutable helper source or a moving `HEAD`,
re-verifies that binding, and builds the captured export offline with an
isolated home and Go cache. Run it only from an independently reviewed commit.
The link-time receipt blocks accidental direct invocation but is public and is
not authentication. Any process already running as the operator can read the
operator's mode-`0600` files; use a separate OS identity or credential broker
when same-UID code is not trusted. Authenticated Wrangler is deliberately
not part of the agent surface: the finite API client keeps the token in one
process and admits only the two commands above. The token never enters process
arguments, while Maintainer, GitHub, provider, and Cloudflare Access credentials
cannot cross the boundary. `publish-app` holds a stable local lock, refuses
every conflicting record, creates only a DNS-only A record for
`app.darkfactory.build` to `76.76.21.21`, and verifies the exact settled state.
Agents must not print the file, copy the token into a worktree or site
`.env.local`, or export it into an agent-wide shell. The token must be scoped
to the named account/zone operation, and authentication never supplies mutation
authority that the owner did not separately grant.

This exception does not change the contributor GitHub boundary above or the
normal control-plane deployment transaction below. Routine control-plane
production deployment still uses the fixed Maintainer-App-dispatched workflow
and its protected GitHub environment secret.

Deployment remains narrow: the Maintainer MCP can dispatch only the fixed
default-branch workflow at an exact commit and reviewed tree. For headless
steady-state operation, the protected `production` environment keeps its
main-only deployment-branch rule and secret but has no required reviewers.
The deploy job itself admits only a `workflow_dispatch` whose event-context
`github.actor` and `github.triggering_actor` are both the exact
`dark-factory-maintainer[bot]` identity and whose ref is `main`; these are
GitHub-authenticated facts, not caller-supplied inputs. Requiring the triggering
actor as well prevents a human rerun from inheriting an App-created run's
authority. A workflow copied to another branch cannot reach the environment
secret because of its main-only rule, while the protected default-branch copy
cannot be changed by an ordinary repository actor. Removing the existing
reviewer is a one-time v2 bootstrap setting change, not an Administration API
granted to agents. Never use interactive authentication, write a credential
into a worktree or repository, or expose it through a prompt, command output,
or log.
This is a contributor-agent workflow boundary, not the future Dark Factory
product GitHub integration. Human operators may continue to use their normal
Git and GitHub CLI configuration for separately reviewed human actions.

## Deploying the control-plane

`maintainer.darkfactory.build` runs the `dark-factory-control-plane` Worker.
Deploy it through the typed Maintainer operation, which dispatches the **Deploy
control-plane** workflow from the live default branch with its exact commit,
reviewed tree SHA-1, and complete durable-request digest. GitHub's returned run
ID is re-read before the operation completes and must name that exact workflow,
commit, and digest. The run refuses a raced default-branch commit before
deployment and proves the checkout matches the reviewed tree, then runs
`control-plane/scripts/local-ci.sh`, uploads a version, proves every authority
secret was inherited, promotes it, verifies `/healthz` and `/readyz`, and rolls
the previous version back if the live check fails.

Routes are a non-versioned Cloudflare setting, so promoting a version swaps code
atomically behind the attached hostname. Detaching the production hostname to
ship a code change is a mistake, not a procedure — it was briefly documented as
one, and it causes an outage for no benefit.

Worker secrets persist across versions, so a routine deployment needs only
`CLOUDFLARE_API_TOKEN`. The GitHub App private key and the webhook secret are
needed for first activation and rotation alone, and never for shipping code.

The credential lives as an environment secret on the `production` GitHub
environment. That scopes it to jobs naming the environment, while deployment
branches restricted to `main` prevent a dispatch from selecting an unreviewed
workflow ref. The job's exact Maintainer App actor check also prevents an
ordinary repository actor from consuming the secret through a manual dispatch
or rerun. The job uses an ephemeral hosted runner. Protected-main review,
the exact-tree workflow assertion, and the Maintainer operation's live-default
binding replace the old per-deployment reviewer pause; retaining that pause
would make every otherwise autonomous deployment require an operator click.

`/readyz` is the deployment's real proof: it returns ready only when the Durable
Object answers, the GitHub App authority verifies, and Cloudflare Access serves
a usable signing key. An unauthenticated `/mcp` 401 proves nothing about Access,
because the handler rejects a missing header before making any network call.
The deployment gate requires the headless readiness label, so a promoted build
must also retain the exact Access service-token binding used by the coordinator.

Public state may include a milestone, exact ref/SHA, checks, links, and next
operator action. Attempt identities, prompts, guidance, raw provider output,
credentials, messages, source, and review deliberation stay private.

## Release and install

Release and install are paused until the independent exact-main boot review.
Do not tag, publish, update the Homebrew tap, run
`factoryctl update --install`, or load a development binary into the operator
job.

After boot approval and a separate release decision, the release transaction
retains this shape:

- the typed Maintainer operation publishes an immutable semver tag only at the
  live default head; the tag-triggered workflow builds the supported archives;
- initial observation binds the tag and source SHA to exactly one tag-push run
  of the fixed workflow and requires the release, when present, to contain only
  the five uploaded assets with GitHub-reported SHA-256 digests;
- a failed exact tag can be recovered only through the fixed release workflow;
  the request binds the exact default-branch workflow commit, the workflow
  refuses a raced dispatch head, and the returned run ID is read before durable
  completion; recovery observation reads that same run while re-proving its
  full-request digest, workflow commit, and immutable tag;
- published manifests and archives carry exact SHA-256 identities;
- install stages a complete version directory, verifies every binary, then
  atomically repoints `bin/current`;
- managed daemon reload must prove the expected launchd PID and exact active
  sibling executables;
- failure restores the previous pointer/job and verifies old health; and
- migrations run at daemon start, with backup/rollback handled before an
  irreversible schema boundary.

Any updater must respect durable run resources and finalization rather than
assuming a daemon restart leaves provider work independently alive.

## Exact reporting

Report each command actually run and whether it passed, failed, or was not run.
Keep local proof, hosted CI, review approval, merge, release, install, and live
verification distinct. A source build is not provider validation; deterministic
shell proof is not Claude/Codex proof; a merged change is not a boot decision.
