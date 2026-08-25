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
   Diagnosing the running service does not justify holding any of them: run the
   Worker locally with `wrangler dev --env-file` and a throwaway key, outside
   this tree, and read the console directly. A production deployment cycle is
   never the right debugging loop.
3. Expose typed, policy-checked operations only. Do not add a generic GitHub
   REST or GraphQL proxy, a shell-command surface, or a fallback to personal
   GitHub credentials. Contributor agents reach the deployed service only as
   the provider-neutral streamable HTTP MCP at
   `https://maintainer.darkfactory.build/mcp`; no Codex-, Claude-, or other
   model-provider GitHub connector is an equivalent authority path. Their
   first remote call must be `maintainer_status`, and they must fail closed
   unless it binds `dark-factory-build/dark-factory` with numeric repository
   ID `1335380107` and permission revision `maintainer-operations-v1`. A
   credential-isolating host transport authenticates the connection; provider,
   tool, and shell processes never inherit the Access pair, and agents never
   read or source `.env.txt` or handle either value.
   The deployed surface currently has six operations: status, exact commit
   publication, pull-request creation, exact-head review submission, check
   observation, and merge-queue enqueue. Missing issue, release, workflow, or
   merge-result operations are human handoffs, never credential fallbacks.
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
