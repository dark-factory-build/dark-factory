# Dark Factory control plane

This directory is the self-contained staging export for the future
`dark-factory-control-plane` service. It is not a `dark-factory` workspace
member and never links to or runs inside `factoryd`.

The service is a Rust Cloudflare Worker backed by SQLite Durable Objects. It
keeps GitHub App credentials outside agent processes and exposes only reviewed,
repository-bound operations over MCP. Deployment state is verified separately;
source control is not evidence that a route, Access policy, or App
configuration is live.

## Current surface

- `GET /healthz` proves only that the Worker can answer.
- `GET /readyz` returns 200 only when the three webhook authority bindings are
  valid and the Durable Object binding, SQLite schema, and migration marker
  pass their audit. When App authority is configured, readiness also imports
  the private key, signs an App JWT, and verifies the exact live installation.
  A partial or syntactically invalid App-authority group makes the whole Worker
  inactive.
- `POST /v1/github/maintainer/webhook` accepts only a bounded GitHub webhook.
  It verifies `X-Hub-Signature-256` over the exact body with HMAC-SHA-256,
  limits the body to 64 KiB, requires one value for every security header,
  requires an `integration` target, and binds the configured App ID.
- A valid `ping` is the only acknowledged event. When the six operation-authority
  bindings are present, acknowledgement also requires an RS256 App JWT, one
  exact selected-repository installation for the configured repository name,
  numeric repository ID, and numeric owner. Every other authenticated event is
  journalled as `policy_rejected`
  and returns 422. No payload can create a task, message, prompt, provider run,
  or GitHub mutation.
- `POST /mcp` is a stateless Streamable HTTP MCP JSON-RPC endpoint. It is
  installed only with the complete operation authority and accepts requests
  only after Cloudflare Access has supplied one authenticated JWT assertion
  identifying exactly one of two configured principals: the operator's email
  identity, or — when `DARK_FACTORY_CLOUDFLARE_ACCESS_SERVICE_TOKEN_ID` is
  bound — one exact Access **service token**, which is how the surface is
  reached headlessly with no human present. Each principal's claim set rejects
  the other's shape, so neither can take the other's path. It exposes six
  typed tools: `maintainer_status`, exact-head pull-request creation,
  exact-head `ALLOW`, `COMMENT`, or `REQUEST_CHANGES` review submission,
  exact-head check-run observation, exact-head commit publication, and
  exact-head merge queue enqueue. `ALLOW` is the repository's own verdict, not
  a GitHub review state -- the App opens the pull requests it would otherwise
  approve and GitHub refuses a self-approval -- so it is posted as `COMMENT`
  carrying a `Dark-Factory-Review:` line the App renders and refuses in caller
  text. The `review` status check reads that line to enforce AGENTS.md rule 2;
  see `docs/development/WORKFLOW.md`. There is no direct-merge tool: a required
  merge queue makes GitHub refuse `PUT /pulls/{n}/merge` outright, and
  `docs/development/GITHUB_APP.md` had already ruled it out ("the broker does
  not ... expose direct merge as a fallback"). Enqueue is the only automated
  path to `main`. Publication refuses the `.github` authority tree, and both
  writes are bound to a stated head commit and to a durable operation ID.
  There is no generic GitHub proxy, arbitrary URL, repository selector, issue,
  shell, or credential-returning tool, and no way to move a ref other than
  forward from the head the caller stated.
- The product webhook and operator/PWA namespaces have no routes.

Missing, empty, partial, or syntactically invalid authority produces the fixed
inactive router: liveness remains 200, readiness is 503, and the webhook route
is not installed. An unusable key, live GitHub drift, or storage failure keeps
the configured route fail-closed and returns 503 without acknowledging a
delivery. Responses never contain configuration, GitHub, or storage errors.

## Durable replay and operation model

The Worker binds one `MaintainerDeliveryJournal` SQLite Durable Object
namespace. Object names include the configured App ID and one byte of the
SHA-256 digest of the GitHub delivery ID. This gives 256 deterministic shards
per App: every use of one delivery ID reaches the same strongly consistent
object without turning the whole service into a global singleton.

Each shard creates the reviewed SQLite schema on first use, records the exact
migration digest, and audits both stored table definitions and the migration
row before readiness or delivery work. The delivery ID is the primary key. An
insert uses `ON CONFLICT DO NOTHING`, then compares the complete stored replay
identity: hook ID, App target, target type, event, parsed action, body digest,
disposition, and webhook-secret revision. An exact replay returns its stored
result; any changed binding returns 409.

Durable Object serialization and the unique key make concurrent exact replay
collapse and conflicting-body behavior deterministic. SQLite state is not part
of a Worker code rollback; deployment and data rollback remain separate
operator decisions. Cloudflare documents SQLite Durable Objects as strongly
consistent and transactionally isolated, with encrypted storage and point-in-
time recovery. See the official [SQLite storage API], [Durable Object rules],
and [data security] documentation.

Maintainer writes use a separate reviewed table in the same Durable Object
namespace. The UUID operation ID is bound to the SHA-256 digest of the complete
typed request and one fixed operation kind. Its state machine is
`planned -> executing -> completed`; `indeterminate` records an ambiguous
external outcome. An atomic transition gives exactly one caller permission to
invoke GitHub. A retry with the same ID and request replays the completed result
or reconciles against the operation marker and exact commit IDs. A different
request under the same ID conflicts. If an executing or indeterminate operation
cannot be reconciled, it is never blindly submitted again.

GitHub App installation tokens are minted only inside the Worker, scoped to the
configured numeric repository, and downscoped per operation. They are held in
zeroizing memory and are never returned or journalled. The permanent App may
have additional installed capabilities, but readiness requires the minimum
checks-read, contents-read, metadata-read, and pull-requests-write set; unused
App-level authority is never copied into an operation token.

[SQLite storage API]: https://developers.cloudflare.com/durable-objects/api/sqlite-storage-api/
[Durable Object rules]: https://developers.cloudflare.com/durable-objects/best-practices/rules-of-durable-objects/
[data security]: https://developers.cloudflare.com/durable-objects/reference/data-security/

Native SQLite remains behind the non-default `development-sqlite` feature. It
is a fast causal model for the webhook contract and deliberately cannot make
readiness succeed. Production contains no Postgres, Neon, database URL,
runtime database role, Vercel adapter, or provider management API.

## Cloudflare configuration

`wrangler.toml` deliberately has:

- `workers_dev = false`;
- `preview_urls = false`;
- no route or custom domain;
- one capability-style Durable Object binding; and
- eleven required production secret bindings.

The exact required bindings are:

- `DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET`: the byte-exact GitHub webhook
  secret, 32-1024 bytes;
- `DARK_FACTORY_MAINTAINER_WEBHOOK_SECRET_REVISION`: a bounded lowercase
  revision such as `maintainer-v1`; and
- `DARK_FACTORY_MAINTAINER_APP_ID`: the positive numeric App ID expected in
  `X-GitHub-Hook-Installation-Target-ID`;
- `DARK_FACTORY_MAINTAINER_PRIVATE_KEY_PKCS8`: standard-base64 encoding of the
  App's unencrypted PKCS#8 DER private key;
- `DARK_FACTORY_MAINTAINER_PERMISSION_REVISION`: exactly
  `maintainer-operations-v1` for this authority revision;
- `DARK_FACTORY_MAINTAINER_REPOSITORY`: the exact safe `owner/repository`
  name;
- `DARK_FACTORY_MAINTAINER_REPOSITORY_OWNER_ID`: the exact positive numeric
  owner ID;
- `DARK_FACTORY_MAINTAINER_REPOSITORY_ID`: the exact positive numeric repository
  ID; and
- `DARK_FACTORY_MAINTAINER_OPERATOR_EMAIL_SHA256`: lowercase SHA-256 of the one
  Cloudflare Access operator email after ASCII lowercasing;
- `DARK_FACTORY_CLOUDFLARE_ACCESS_TEAM_DOMAIN`: the exact lowercase
  `https://<team>.cloudflareaccess.com` issuer and key origin; and
- `DARK_FACTORY_CLOUDFLARE_ACCESS_AUD`: the exact lowercase 64-hex application
  audience tag.

One further binding is deliberately outside the all-or-nothing group:

- `DARK_FACTORY_CLOUDFLARE_ACCESS_SERVICE_TOKEN_ID`: the exact
  `<32-hex>.access` client ID of the one Access service token allowed to act
  headlessly. Absent, every service-token assertion is rejected and only the
  operator identity can reach `/mcp`; it never means "any service token".
  Because it is optional and inherited across versions, `/readyz` names which
  principals are live — `mcp_six_tools_operator_and_headless` or
  `mcp_six_tools_operator_only` — and the deployment gate requires the former.

The revisions and numeric IDs are stored as secrets too. They are not
confidential, but treating all authority settings identically avoids a
dashboard/source split and makes a missing value block upload or deployment
through Wrangler's required-secret validation. The App-authority group is
all-or-nothing at runtime. There are no aliases or ambient fallbacks.

A Cloudflare Access application must protect the exact `/mcp` application path
before the route receives traffic. A service token needs a `non_identity`
("Service Auth") policy on that application: an ordinary `allow` policy does
not match a service token and silently falls through to interactive IdP login,
which a headless caller cannot complete.

The Worker also validates the injected JWT independently: it fetches the
bounded key set from the configured team domain, matches one RS256 signing key
by `kid`, verifies the signature with WebCrypto, and binds issuer, single
audience, token type, and time window. It then binds the identity to exactly
one principal — for the operator, the JWT email, the injected email header, and
the configured digest; for a service token, `common_name` against the
configured client ID, requiring the email claim to be absent. A service-token
assertion omits `nbf` and `email` and carries `aud` as a bare string rather
than an array; all three are accepted shapes, and `aud` is still compared for
exact equality, never containment. An unprotected route still cannot forge any
of those claims.

Never put values in a checked-in file, `.env*`, `.dev.vars*`, a provider
process, Dark Factory state, shell history, or the macOS Keychain. Cloudflare
secret values belong only in the platform secret binding. The Durable Object
binding embeds resource authority without exposing a resource credential to
the Worker. See Cloudflare's [binding] and [secret] documentation.

[binding]: https://developers.cloudflare.com/workers/runtime-apis/bindings/
[secret]: https://developers.cloudflare.com/workers/configuration/secrets/

`wrangler secret put` immediately creates and deploys a Worker version. It is
therefore not an acceptable staging command for this bootstrap. The reviewed
live runbook must use the versions API or another no-traffic staging mechanism,
prove the exact draft, and add a route only after the independent deployment
gate. Do not improvise the first live sequence from generic Wrangler examples.

## Local proof

Rust is pinned to 1.88 and Node 22 or newer is required. Wrangler, `worker`,
`worker-build`, and `wasm-bindgen` are pinned because their generated
interfaces must agree. Run:

```sh
./scripts/local-ci.sh
```

The authoritative gate performs:

1. Rust format and native all-feature Clippy;
2. the fixed inactive default tests;
3. the native SQLite replay, conflict, signature, header, body-limit, and
   policy tests;
4. production-target `wasm32-unknown-unknown` Clippy;
5. a release Worker build;
6. a non-uploading `wrangler deploy --dry-run`; and
7. a local `workerd` integration proof, including readiness, signed ping,
   exact concurrent replay, concurrent conflict, policy rejection, duplicate
   headers, the 64 KiB limit, persistence across runtime restart, absent future
   routes, and invalid-config inactivity. The native SQLite lane separately
   proves operation replay, conflict, state transitions, and exactly one
   concurrent effect claim. Live GitHub and Access calls are not made by CI.

The gate installs `worker-build` 0.8.5 under ignored `.tools/` and uses the
project-local locked Wrangler. It creates no `.env` file, calls no live API,
and deploys nothing.

## Activation gates

No live action is implied by merging this code. Activation requires a separate
operator-authorized run in this order:

1. local CI, release bundle, Wrangler dry-run, and independent adversarial
   `ALLOW` on one exact commit;
2. a disposable Cloudflare account or isolated Worker proof using distinct
   test bindings and no production GitHub App;
3. review of the exact no-traffic secret-staging and route/domain commands;
4. verification of the permanent `Dark Factory Maintainer` GitHub App and its
   exact webhook secret;
5. upload of one exact Worker version with required secrets while it has no
   public route;
6. configuration of a Cloudflare Access self-hosted application for the exact
   `/mcp` path, with Managed OAuth and only the configured operator identity;
7. verification of the version, Durable Object binding, Access application,
   and deployment configuration without reading secret values;
8. a route activation followed by `/healthz`, `/readyz`, signed ping, replay,
   conflict, unauthenticated MCP rejection, authenticated MCP initialization,
   and `maintainer_status`; and
9. connection of the remote MCP server to Codex/ChatGPT, followed by use of its
   typed tools for the next PR under the normal independent-review rule.

Cloudflare account credentials, GitHub App credentials, route changes, and
deployments remain operator authority. Tests and review never exercise them.

## Future integrations

This hosting choice does not turn the control plane into a generic webhook
proxy. Each future source gets an explicit verifier, bounded schema, policy,
and storage namespace. A product delivery may at most become the existing
provider-neutral quarantine envelope; it never becomes executable work by
itself.

High-volume durable receipt can later hand off to Cloudflare Queues only after
the Durable Object commit. Queues are at-least-once, so the Durable Object
identity remains the deduplication authority. Long-running typed effects may
later use Workflows, but every external GitHub mutation still needs a durable
operation key and reconciliation state for ambiguous outcomes. Those are
implemented for the first pull-request operations; each future mutation still
requires its own reviewed schema, policy, reconciliation query, and tests.
