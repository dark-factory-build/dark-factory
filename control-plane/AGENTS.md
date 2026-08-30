# Agent instructions

This file is the canonical guidance for the `dark-factory-control-plane`
repository. The service is a provider-neutral authority boundary for Dark
Factory's GitHub Apps. It is deployed separately from `factoryd`; neither
Codex nor Claude owns its credentials or its durable journal.

## Critical rules

1. Work on a branch in an isolated worktree, never directly on `main`. Open a
   pull request and require an adversarial review by someone other than the
   author before merge.
2. Keep runtime credentials outside agent processes and Dark Factory state.
   Store them only in the deployment platform's secret manager — Cloudflare
   Worker secrets for the running service, and a GitHub Actions environment
   secret for the credential that deploys it; never put a GitHub App private
   key, installation token, webhook secret, or database URL in a prompt, a
   checked-in file, an ambient `gh` session, or the macOS Keychain. Worker
   secrets persist across versions, so a routine deployment promotes a new
   version with only the Cloudflare API token and never handles the App private
   key or the webhook secret; those are needed for first activation and
   rotation alone.
   The local Cloudflare API token is separate from those runtime bindings. For
   an explicitly owner-authorized DNS operation, invoke the exact command
   through the parent repository's
   `../scripts/with-cloudflare-env.sh dns status` or
   `../scripts/with-cloudflare-env.sh dns publish-app`. Run these from the
   `control-plane/` directory; from the repository root, omit `../`. Its
   compiled boundary selects only `CLOUDFLARE_API_TOKEN` and
   `CLOUDFLARE_ACCOUNT_ID` from the ignored, mode-`0600` root `.env.txt`. Its
   launcher replaces itself with an
   explicit empty environment before any setup child, and the operation does
   not pass the selected values to Wrangler or another child process. It
   captures and re-verifies one exact commit, refuses mutable helper source,
   and builds the implementation from an offline Git export. Its direct-
   invocation check is a guardrail, not authentication or a same-UID security
   boundary. Run untrusted same-UID code under a separate OS identity or keep
   the credential behind a broker; never copy the file or token into this tree.
   Diagnosing the running service does not justify holding any of them. For
   local diagnostics, use the existing non-production `--var` fixtures only
   through `scripts/with-clean-wrangler-env.sh`; the integration suite is the
   canonical invocation. Read the console directly. Do not run bare
   `wrangler dev`, use `wrangler dev --env-file`, the root `.env.txt`, a
   production token, Wrangler OAuth, or keychain state for local debugging. A
   production deployment cycle is never the right debugging loop.
   Owner-authorized activation may run only through the independently reviewed
   parent command `../scripts/bootstrap-maintainer-v2.sh <reviewed-tree>`,
   which ships the working tree and so proves `HEAD:control-plane` is the exact
   tree the operator names. Routine deployments use the fixed Maintainer-App
   workflow; this path is for when that workflow cannot run, which
   `dispatch_control_plane_deploy` observing the repository makes possible --
   a defect there disables the App's ability to deploy its own repair, and the
   workflow admits no human dispatch in its place.
3. Expose typed, policy-checked operations only. Do not add a generic GitHub
   REST or GraphQL proxy, a shell-command surface, or a fallback to personal
   GitHub credentials. Contributor agents reach the deployed service only as
   the provider-neutral streamable HTTP MCP at
   `https://maintainer.darkfactory.build/mcp`; no Codex-, Claude-, or other
   model-provider GitHub connector is an equivalent authority path. Their
   first remote call must be `maintainer_status` for the exact repository they
   intend to act on, and they must fail closed unless it returns that same
   `owner/name`, a positive numeric repository ID, and permission revision
   `maintainer-operations-v2`. Every tool names its repository; the App reaches
   only repositories it is installed on, and nothing selects one implicitly. A
   credential-isolating host transport authenticates the connection; provider,
   tool, and shell processes never inherit the Access pair. Agents never read
   or source `.env.txt` directly or handle either Access value; the parent
   Cloudflare helper's two-variable CLI boundary is the only exception and
   never exposes the Access pair.
   The deployed surface is finite: authority and default-head observation,
   durable operation observation, bounded issue lifecycle, exact commit and
   pull-request publication, exact-head review and CI diagnosis/recovery,
   merge-queue enqueue and eventual-result observation, immutable release
   publication/observation/recovery, and one exact control-plane deployment
   dispatch. GitHub's delete-on-merge setting owns source-branch cleanup.
   Direct merge and generic issue, ref, release, Actions, or API operations are
   absent, never credential fallbacks.
4. Treat webhook authentication and replay handling as load-bearing. Verify
   the signature over the exact bounded request body, require exactly one of
   every security header, bind the configured App ID, and journal the full
   replay identity atomically before acknowledging a delivery.
5. Keep the bootstrap inert. Only a signed GitHub `ping` may be acknowledged
   until a separately reviewed typed event contract is implemented. An
   authenticated but unsupported event is a policy rejection, not work.
6. Keep liveness and readiness distinct. `/healthz` reports only that the
   process can answer; `/readyz` succeeds only when every dependency required
   by the active surface is configured and live. Missing or invalid authority
   must fail closed.
7. Keep the three planes separate: maintainer operations, product webhook
   intake, and the operator/PWA API. A product delivery can at most become a
   quarantined input envelope; it must never directly become a task, prompt,
   provider run, or GitHub mutation. Browser clients never receive raw
   deliveries or GitHub credentials.
8. Prefer deletion and direct implementations over speculative abstraction.
   Update tests and documentation in the same change when behavior changes.
9. Rust 1.88 is the pinned toolchain. Run `./scripts/local-ci.sh` before
   finishing; it is the authoritative local and hosted source gate.
10. Deployments and credential changes require an explicit task. Never send a
    provider prompt, mutate the Dark Factory live install, use ambient
    Keychain authentication, or deploy as a side effect of tests or review.
    Deploy through the dispatched `Deploy control-plane` workflow, which binds
    the run to a stated reviewed tree, proves secret inheritance before traffic
    moves, and rolls back a failed live check. Routes are a non-versioned
    setting, so promoting a version never detaches the production hostname:
    taking the route down to ship a code change is a mistake, not a procedure.
11. Report exactly which checks, deployments, migrations, and live probes did
    or did not run. Local proof is not hosted CI or production proof.
