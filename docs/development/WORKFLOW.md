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
3. Run focused checks through the shared lease when they invoke Cargo or
   process-sensitive fixtures:

   ```sh
   ./scripts/with-local-ci-lease.sh cargo +1.88.0 test -p factoryd --lib
   ```

4. Run the authoritative gate on the exact head:

   ```sh
   ./scripts/local-ci.sh
   ```

   Ubuntu x86-64 contributors use `./scripts/local-ci.sh --linux-source`.
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
wrapper for a load-bearing Cargo or process fixture. Set
`DARK_FACTORY_LOCAL_CI_WAIT=0` to refuse instead of waiting.

The gate clears inherited live-factory home, socket, and attempt identity
variables. Tests set their own isolated values. The build-headroom preflight
reports and refuses low space but does not reclaim anything; inspect only
inactive regenerable Cargo targets manually. Product Rust verification uses
its own bounded daemon cache. It does not replace this daemon-independent
development lease.

`crates/factoryd/tests/rust_completion.rs` drives the product's Rust
completion lane through the production manager, so the gate compiles a
generated one-crate workspace with the exact toolchain running the suite. It
needs `CARGO` to name a real toolchain directory holding both `cargo` and
`rustc` — `cargo +<version> test` provides that — and it needs the workspace
binaries, so run it as part of the whole-workspace gate rather than with
`-p factoryd` alone. Everything it compiles lives under its own temporary
root. It costs roughly twenty seconds: the daemon wakes its Rust maintenance
queue at the transitions it observes rather than only on the reconcile tick,
so what is left is Cargo plus that interval's bounded polling for exact
process absence.

## Isolated daemon checks

Use a second, throwaway home and explicit socket. Never rely on the default:

```sh
export DARK_FACTORY_HOME="$(mktemp -d /tmp/df-dev.XXXXXX)"
chmod 700 "$DARK_FACTORY_HOME"
target/debug/factoryd --socket "$DARK_FACTORY_HOME/f.sock" &
target/debug/factoryctl --socket "$DARK_FACTORY_HOME/f.sock" health
```

Worker lifecycle checks must use the deterministic shell provider and a tiny
temporary Git repository. They must prove the provider receives one
daemon-owned `.git`-free Change and that the same run and source survive the
injected boundary. Do not point any fixture at a real provider.

A lifecycle fixture must register resources before use and verify after its
test that exact descendants and disposable paths are gone. Crash/restart tests
must restart the daemon and let its durable finalizer converge. `Drop`, shell
traps, sleeps, broad process scans, and cleanup owned only by the killed fixture
are insufficient proof.

The scratch-only macOS smoke covers these cuts through external causal proofs;
it makes no claim about launchd or the operator's installed job.

The separate opt-in `./scripts/macos-launchd-release-proof.sh` uses a randomized
scratch-only launchd label to prove release replacement and rollback. The
fixture job is not an attempt `KernelResource`; the installed
`com.dark-factory.factoryd` label and plist are observed before and after and
must be unchanged.

That job is contained by a durable receipt, not by the script. Before
`launchctl bootstrap`, the fixture records the domain, label, private root
identity, and staged digest under a ledger that outlives one run
(`DARK_FACTORY_LAUNCHD_GATE_LEDGER`, `$TMPDIR/dark-factory-launchd-gate` by
default). The coordinator resumes that ledger before and after the fixture, so
a run killed at any point — target, fixture, or coordinator — is finalized by
the next one rather than by a trap or a background verifier that dies with its
parent. Ownership is an advisory lock the kernel releases on death, so a
resume never tears down a job another live coordinator is using.

Finalization boots out the exact label, proves absence only from launchctl's
documented not-found classification, waits for every recorded PID, revalidates
the root's device, inode, owner, and claim marker, and only then removes it.
Anything unproven keeps the root and fails visibly; recover by fixing the
cause and re-running, which resumes the same receipt. A receipt that cannot be
finalized makes every later run fail at startup on purpose, since it may
describe a job that is still loaded. That is deliberately conservative: it also
fires when the receipt merely cannot be acted on, such as an unreadable file or
a recorded PID whose number has been reused. If the service the receipt names
is provably absent, remove that file from the ledger to unblock later runs.

Containment itself is proved on every platform, including Linux, by
`cargo test -p factoryctl --test launchd_gate` against a fake `launchctl` —
including a coordinator that is `SIGKILL`ed after bootstrapping. Hosted macOS
runners get a fresh `TMPDIR` per job, so cross-run resume is exercised by those
tests and by local dogfooding rather than by CI.

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

SQLite migrations are sequential numbered files under
`crates/factoryd/migrations/`. Never edit a shipped migration. Historical
fixtures must apply the real ordered chain to version N rather than creating a
new schema and manually deleting objects.

Kernel cutover migrations refuse databases containing live legacy authority or
other external effects whose completion cannot be proven. Preserved source
paths migrate into metadata-only `legacy_sources` quarantine, including
separate records when agents or projects shared a path. Factoryd never inspects
or owns those paths; forgetting a record does not touch the filesystem. Before
an operator database crosses an irreversible migration boundary, take an
explicit backup and rollback decision. Migration never implies boot approval.

## CI and GitHub

The pull-request workflow runs the shared source gate on hosted macOS and the
Linux source-only lane. The aggregate `required` context is the merge gate.

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
Everything else -- `crates/`, `control-plane/`, the rest of `scripts/`, and
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

`review` is part of the `required` aggregate, which is what makes rule 2 a
merge condition rather than a convention:

```yaml
needs: [checks, linux, control-plane, review]
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
apply rather than what is applied, and has already drifted from it once. Four
facts are required: the `merge_queue` rule, its `ALLGREEN` grouping, the
`required_status_checks` rule, and the `required` context within it. If any
lapses, `merge_group` stops firing or stops gating, the review gate never runs,
and `required` would otherwise stay green while rule 2 quietly stopped being
enforced. Rulesets in `evaluate` or `disabled` enforcement are not returned by
that endpoint, so a ruleset downgraded out of enforcement reads as absent —
which is the answer we want.

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

Agent automation must use an explicitly authorized credential broker or
App-backed tool surface supplied by its host for every authenticated remote
read or write. This includes private checkout and fetch as well as publishing
refs, opening or updating pull requests, reviewing, merging, and deleting
refs. The current coordinator's connected GitHub App is one such surface;
Claude or another harness may provide an equivalent. Agents must not use
ambient `git fetch`, `git pull`, `git clone`, `git push`, `gh`, `gh auth`, or
SSH-based access: those paths can consult the operator's credential helper,
login keychain, SSH agent, or other user credential state.

Three narrow carve-outs exist: `gh issue` (create, comment, close, reopen,
list, view -- not `edit`, because a canonical issue body is an immutable
source revision); authenticated read-only `gh` (`pr view`/`checks`/`diff`,
`run view`/`list`, `api` GET); and `git push` to any ref except the default
branch, `--force-with-lease` only. Pushing the default branch, and opening or
merging a pull request, are not covered. See rule 14 in
[AGENTS.md](../../AGENTS.md) for the reasoning behind each.

Anonymous HTTPS reads of public repositories are allowed when credential
lookup and interactive prompting are disabled. A host may instead inject a
short-lived, repository-scoped read credential for checkout without exposing
it to the agent. If no authorized surface is available, authenticated access
stops without contacting the remote. There is no token fallback for agent
*contribution* writes: operator approval does not authorize injecting
`GH_TOKEN`, `GITHUB_TOKEN`, a personal access token, or another write
credential into an agent process to push branches, publish commits, or open
pull requests. Those go through the App surface, or a human performs them.
Deployment is the one exception, and it is narrow: see `AGENTS.md` rule 14 and
"Deploying the control-plane" below. Never use interactive authentication,
write a credential into a worktree or repository, or expose it through a
prompt, command output, or log.
This is a contributor-agent workflow boundary, not the future Dark Factory
product GitHub integration. Human operators may continue to use their normal
Git and GitHub CLI configuration for separately reviewed human actions.

## Deploying the control-plane

`maintainer.darkfactory.build` runs the `dark-factory-control-plane` Worker.
Deploy it by dispatching the **Deploy control-plane** workflow with the reviewed
tree SHA-1; the run proves the checkout matches that tree, runs
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
environment. That scopes it to jobs naming the environment, which is necessary
and not sufficient: a dispatch runs the workflow file from the ref it is
dispatched against, and `dark-factory-mac` is a persistent runner shared with
CI (#54). Both gaps are closed by settings on that environment — **required
reviewers** and **deployment branches restricted to `main`** — which the
workflow file cannot assert for itself. Without them the credential is only as
protected as write access to the repository.

`/readyz` is the deployment's real proof: it returns ready only when the Durable
Object answers, the GitHub App authority verifies, and Cloudflare Access serves
a usable signing key. An unauthenticated `/mcp` 401 proves nothing about Access,
because the handler rejects a missing header before making any network call.
Proving authenticated MCP end to end needs an Access service token and a policy
that includes it; until then that leg is verified by hand.

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

- a semver tag matching the workspace version builds the supported archives;
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
