# The operational factory

Dark Factory pictures a running software system as a production plant. The
plant shows what the software does. The workers show what agents are doing to
it. This document is the contract for that picture. It replaces the
source-topology floor (removed with `FACTORY_FLOOR.md` in this cutover).

Two rules govern everything below:

- **Static analysis describes what could happen. Runtime observation
  describes what we observed happening.**
- **Absence of telemetry means unknown, not inactive.**

## 1. What the old floor was

The old floor drew the repository's directory and package tree.

- `internal/topology` walked a `git archive` of each registered repository's
  target ref. It emitted `repository/module/package/directory` nodes, Go and
  `package.json` import edges, and per-directory file inventories.
- `internal/daemon/topology.go` grouped repositories under a synthetic
  project root.
- `TOPOLOGY` frames carried that tree to the browser.
- `console-view.ts` chose up to 96 "rooms" from it and called their
  descendants "assemblies".
- `scene.ts` packed the rooms into a grid and sized equipment by file count.
- `factory-scene.tsx` drew the result. Import cables ran through corridors.
  Name-based "responsibility" motifs were guessed from file names.

Work was mapped onto the tree by changed paths. `RUN_PATHS` sampled a running
Change's touched directories, and the UI matched those to the deepest room.

Everything that brings the floor to life was independent of that tree:
movement, idle life, the break room and cat, messages and peer questions,
sprites, worker identity, reviewers and the production panel. That code
survives.

What did not survive was the meaning. A room was a directory. It said nothing
about whether the software could receive a request, where that request went,
whether it was happening, or whether we could even tell.

## 2. Keep, replace, delete

| Keep (reconnected to the new world) | Replace | Delete |
| --- | --- | --- |
| Worker sprites, identity, appearance, sprite editor | `layoutScene`, `composeRoom`: rooms from directories | `internal/topology` graph, IDs and inventories |
| `movement.ts` routing over doors and corridors | `wires()`: import cables become flow lines between machines | `internal/daemon/topology.go`, the `projectTopology` wire builder |
| `idle-life.ts` (cat, chatter), `messages.ts` (paper, peer pulses) | `console-view.ts` floor selection: units and halls replace room selection | `TOPOLOGY_GET` / `TOPOLOGY`, client decode, fixtures |
| Break room, errands, commons, its board/Missions/Tasks/Library implements | `runFootprint`: changed paths now map to operational nodes | Name-based `responsibility()` motifs, file-count `equipmentScale` |
| Reviewer pseudo-workers | Proposal relationships: import deltas give way to changed operational nodes | Inventory inspector, `source_files`, the "detail" grouping setting |
| `RUN_PATHS` sampling, the production panel and its delivery data | Floor legend and help | `FACTORY_FLOOR.md` |
| Camera, scrolling, search, inspector shell, reduced motion | | Knowledge `entities` validation and context paths now resolve through operational node IDs and their sources (`kernel/knowledge.go`, `daemon/knowledge.go`, `daemon/knowledge_context.go`, the Library form). The live database held zero entity references on 7 Oct 2026 (5 revisions checked). |

`Classify` (file kind by name) and `BuildArchive` (exact-revision source)
move into the new package because the inference needs them. Import analysis
is deleted. It described code structure, not operation.

## 3. Reusing analysis infrastructure

Tools were compared on whether they reveal operational structure (routes,
schedules, queues, datastores, outbound systems), not general code
navigation.

| Tool | Verdict |
| --- | --- |
| Go `go/parser` + `go/ast` (stdlib compiler front end) | **Used.** Exact Go syntax with no build: listeners, `ServeMux` patterns, `exec` targets, URL literals, driver imports. |
| Framework and platform config: `wrangler.toml`/`.jsonc`, `vercel.json`, `package.json`, `go.mod`, `Cargo.toml`, Next.js `app/` conventions, compose files | **Used.** Most structure is declared. The extractors parse what the frameworks themselves read. |
| Dependency manifests: `requirements.txt`, `pyproject.toml`, `Gemfile`, `pom.xml`, `build.gradle`, `go.mod`, `package.json`, `Cargo.toml` | **Used.** A small table maps well-known clients to stores, queues and external parties. For example, psycopg or pg becomes a Postgres store, and celery, sidekiq or bullmq becomes a queue. |
| Deployment declarations: compose services, `Procfile`, `fly.toml`, wrangler, Next.js | **Used.** They declare units in any language. |
| Tree-sitter grammars as WebAssembly, run by wazero | **Used** for TypeScript/JavaScript, Python, Ruby, Java, Kotlin and Rust (`internal/opgraph/treesitter`). The released web-tree-sitter runtime and grammar `.wasm` builds are embedded and pinned by SHA-256, so the result never depends on the machine and the build stays `CGO_ENABLED=0`. Queries shape calls, decorators, annotations, bindings and imports; Go code decides what each shape means per framework, resolves string constants within a file, and follows in-file router mounts. A match is `declared` when the file imports the framework that runs it, else `inferred`. Files over 256 KiB, past 32 MiB per repository, or past a deterministic work budget yield nothing; a 2 s per-file deadline backs the budget for pathologically nested input. Script files a package's entry (`main`, `bin`, `start`) never imports belong to no unit, as Go functions no binary calls do. Reachability follows relative imports only: a file reached solely through a path alias (`@/…`) or a dynamic `require` belongs to no unit. |
| ast-grep | Not adopted. A tool that is optional on `PATH` would make the node set, and so the IDs and layout, depend on the machine. |
| SCIP (scip-go, scip-typescript), gopls, tsserver | Not adopted. Each needs a build or type check. They provide precise references, which an operational graph does not need. Symbol strings embed versions, so they are unstable as identities. |
| CodeQL | Rejected. Its licence restricts it to open-source codebases, and it builds a database in minutes, not milliseconds. |
| Semgrep CE / Opengrep | Rejected. It is a heavy runtime, and Semgrep's maintained rules carry a restrictive licence. The tree-sitter queries cover the same cases. |
| stack-graphs | Rejected: archived in 2025. |

The bespoke part is small and declarative. Each extractor maps a framework's
own declaration to node kinds and runtime selectors. No parser or tracer is
written.

## 4. The Operational Graph

`internal/opgraph` holds a small, language-neutral semantic graph. It
contains no rendering concepts: no coordinates, sprites, rooms, conveyors or
animations.

```text
System           one project: one or more repositories
 ├─ Node         id, kind, label, repository, unit, module, sources[],
 │               selectors{}, evidence[]
 └─ Edge         from, to, kind, evidence[]
```

**Node kinds.** There are seven. Static analysis cannot reliably tell a cache
from a store, or an output from an external party, and the picture must not
smuggle in distinctions the evidence lacks.

- `processor`: a deployable unit that runs code (daemon, Worker, web server,
  browser app, CLI binary)
- `ingress`: an entry point. `trigger` is `request` (HTTP route, listener,
  socket, page), `timer` (cron, ticker) or `message`.
- `job`: a background loop inside a unit
- `queue`: a queue or stream
- `store`: a datastore or cache. `db.system.name` says which.
- `external`: a party we call but do not own (an API, a launched process, a
  push service)
- `unknown`: runtime activity that no static node explains

**Edge kinds:**

- `handles`: ingress → processor
- `calls`: processor → ingress or external
- `uses`: processor → store
- `publishes` / `consumes`: queue edges
- `runs`: processor → job

**Identity.** The ID is `sha256` over the length-prefixed system, unit key and
key, truncated to 16 bytes of hex.

- The key is namespaced by what it names, for example `http:GET /x`,
  `listen:tcp:127.0.0.1:43123`, `host:api.github.com`, `do:FactoryRelay` or
  `go:example.com/cmd/x`.
- Kind is an attribute, not identity, so reclassification keeps the ID. When
  a runtime-only node is later explained statically, the static node takes the
  runtime node's ID through `aliases`.
- Identity does not depend on file layout, labels, counts, revisions or
  activity. Renaming a route is a new node. Moving its handler is not.

**Selectors.** These are the runtime attribute values that identify the
node, spelled with OpenTelemetry semantic-convention keys:

- `service.name`
- `http.request.method` and `http.route`
- `rpc.method`
- `messaging.destination.name`
- `db.system.name` and `db.namespace`
- `server.address` and `server.port`
- `url.template`
- `code.file.path`
- `process.executable.name`

Binding a runtime observation to a node is a selector comparison. No mapping
table is configured.

**Evidence.** Each item records:

- origin: `static` or `runtime`
- extractor or source: `go-ast`, `tree-sitter`, `wrangler`, `nextjs`, `otlp`, `cloudflare`
- a short detail
- `confidence`:
  - `declared`: parsed from a declaration the framework executes
  - `inferred`: a matched code pattern
  - `heuristic`: a literal or name

The node's **evidence state** is derived, never stored:

| State | Rule |
| --- | --- |
| `static` | Only static evidence. |
| `runtime` | Only runtime evidence. This is a gap in the static model. |
| `both` | Static and runtime evidence agree. |
| `uncertain` | All evidence is `heuristic`. |
| `contradicted` | Runtime evidence for this node's selectors names a different unit than static evidence. For example, a route statically owned by A is observed being served by B. |

Edges carry the same evidence and states.

## 5. Multi-repository systems

A **system** is a Dark Factory project. Its registered repositories are its
sources; the site, relay and control plane are not separate demos. Each node
records its repository and source paths, so ownership stays inspectable.
Repository does not decide placement: units are deployment units, and one
repository can hold several units (`dark-factory` holds factoryd, factoryctl,
factory-runner, the relay Worker and the control-plane Worker).

**Units** are `processor` nodes, found language-neutrally first:

- compose services with a `build`, a `Procfile` process, or `fly.toml`
- a wrangler Worker
- a Vercel or Next.js app. This is two units: `server` and `browser`. The
  browser one is where client code runs, and its runtime is observed (or not)
  separately.

Language detail adds units where code is the only declaration:

- a Go `main` package
- a `package.json` with `bin` or a server framework dependency
- a Django `manage.py` or Rails `config.ru`
- a Spring Boot application class

**Last resort.** A repository where nothing is recognised becomes one
processor named after the repository, with an `unknown` ingress and the
detail "no recognised framework". The plant is never silently empty.

Every other node names the unit it runs in. Go code in library packages
belongs to every `main` package that transitively imports it. Code in a
JavaScript library package belongs to the units whose `package.json` depends
on it by name, across repositories.

**Two walkthroughs.**

- **Django + Celery + Postgres with compose:**
  - compose `web` and `worker` become units
  - `db: postgres` becomes a store and `redis` a store
  - `celery` in requirements becomes a queue that both units use (a manifest
    cannot say which side publishes)
  - `urls.py` `path()` entries become ingress, mounted under the routes that
    `include()` them, `declared` when the file imports `django.urls`
- **Rails:**
  - `config.ru` becomes a unit
  - `config/routes.rb` verbs become ingress
  - `pg` and `sidekiq` in the `Gemfile` become a store, a queue and a worker
    job

**Cross-repository linking is generic:**

1. Every unit declares where it can be reached:
   - wrangler routes and custom domains
   - listener addresses
   - Next.js hosts named in its middleware
2. Every outbound URL literal or `fetch` target is matched against those
   declarations.
3. A match becomes a `calls` edge to that unit's ingress, at `inferred`
   confidence. No match becomes an `external` node keyed by host.

Runtime evidence can later add or confirm edges that static analysis cannot
see, for example `server.address` on client spans.

Merging never deletes evidence. The same key seen by two extractors is one
node with two evidence items.

## 6. Runtime observations

Dark Factory renders and reasons locally. It is not the telemetry host. It
pulls aggregates from systems the project already runs, plus an optional
local receiver, and keeps only bounded per-node aggregates in memory.
Restarting it truthfully drops runtime state to `stale`/`unobserved`.

**Provider-neutral observation.** Adapters translate into this shape. The
renderer never sees a provider.

```text
Observation {
  source        adapter id ("factoryd", "otlp", "otlp-remote", "cloudflare", "github")
  scope         local | remote | client | operational
  environment   "local", "production", …
  window        [start, end) in ms
  kind          server | client | consumer | producer | internal | deploy
  attributes    OTel semantic-convention keys (selectors above)
  peer          attributes of the far end, for client/producer spans
  count, errors, latency_p50_ms, latency_p95_ms, backlog
  version       deployed version, for deploy observations
}

Coverage {        "this source is reporting on this unit"
  source, environment
  unit           the processor's service.name or alias
  as_of, ttl     past the TTL the coverage is stale
}
```

`Coverage` turns "quiet" into a claim instead of an inference. A source that
is current for a unit, and whose granularity reaches the node, saw zero
matching observations. That means zero activity. Without coverage, zero
observations means nothing.

Each adapter states its **granularity**: the selector keys it can report. The
granularity is a fixed table in code, not a runtime claim.

| Adapter | Granularity |
| --- | --- |
| `cloudflare` | `service.name` only (per Worker) |
| `factoryd` | `service.name`, `rpc.method`, `url.path`, `process.executable.name`, `server.address` |
| `otlp`, `otlp-remote` | whatever keys a span carried for that service |
| `github` | `service.name`, `cicd.pipeline.task.name` (per workflow job) |

**Adapters, in order of yield:**

- **`factoryd`**: the daemon observes its own listeners: browser messages,
  HTTP requests and local API operations, counted with `rpc.method`,
  `url.path` and `network.transport`. Its outgoing work is client spans:
  each HTTP request (Maintainer, Linear, release check, push, Cloudflare
  pull) by peer `server.address` and `http.request.method`, failing on a
  transport error or a 5xx, never with its URL, path, query, headers or
  body; and each factory-runner or reviewer CLI launch by peer
  `process.executable.name`, failing only when the process cannot start,
  which lands on the unit of that name when the system has one. Each scheduler tick is an internal span with
  `code.function.name`, binding to its timer. SQLite and relay traffic are
  not observed. Only the listeners are claimed as coverage, so silent
  outgoing work reads partial, never idle. Coverage is local and current
  while the daemon runs.
- **`otlp`**: an OTLP/HTTP receiver on the loopback listener
  (`POST /v1/traces`) for any local process of any system. It takes the
  protobuf most SDKs send by default as well as JSON, optionally gzipped,
  folds spans into observations and discards them. It refuses browser
  origins and oversized bodies. Coverage is claimed per `service.name` it has
  seen. Systems already exporting OTel to a vendor need a vendor adapter, so
  OTLP is not where a remote system's truth comes from.
- **`cloudflare`**: the Workers GraphQL Analytics API
  (`workersInvocationsAdaptive`). Requests, errors and CPU time per script are
  generated by the platform, so coverage is claimed per Worker with no
  instrumentation needed.
- **`github`**: CI for any repository the factory publishes to, through the
  Maintainer broker it already holds; no token of its own. Inference reads
  `.github/workflows/*.yml` into one `GitHub Actions · <repository>` unit per repository, named after it
  (`service.name` `github-actions:<repository id>`), each workflow job a job
  node labelled `<workflow> / <job>`. The production refresh already reads
  `observe_pull_request_checks` for each pull head whose checks can still
  change; each check run it stores becomes one observation on the job its name matches (matrix
  suffixes `" (…)"` dropped), failing on `failure`, `timed_out` or
  `startup_failure`. Only jobs of workflows a pull request triggers carry the
  selector, since the broker reads checks of pull request heads alone; a
  push-only workflow's jobs stay partial. Coverage is claimed only when no read
  failed and the pull page was complete. No API call is added.
- **`github` deploys:** GitHub Deployments are written by whatever deploys
  (Vercel's GitHub integration, Actions, any CD tool), so one read covers
  every platform that records them. The broker's `list_deployments
  {repository, per_page ≤ 30}` mints `deployments: read` and `metadata: read`
  and returns each deployment, newest first, as `{id, environment,
  production_environment, sha, ref, created_at, state, updated_at}`, the state
  and time from its newest status (`pending` at `created_at` when it has
  none); no URLs, payloads, descriptions or creators. The production refresh
  calls it once per repository whose plant has a deployed unit. A deployment
  whose newest status is `success` becomes one `deploy` observation (`End` its
  status time) on a unit: the one a `github` source's `services` maps its
  environment to, else, for the production environment, the repository's only
  deployed unit; with several and no mapping it lands on none. A `failure` or
  `error` since the last refresh counts as an error there; other states are
  not drawn, and no coverage is claimed. The production environment is the
  `github` source's `environment`, else the one GitHub flags
  `production_environment`:

  ```json
  {"adapter": "github", "environment": "Production", "services": {"Production": "web"}}
  ```

  The newest successful production deployment also moves the work line: see
  *SHIPPED* in section 13. A customer's installation accepts the
  `Deployments: read` request before its deploys show. Deployments are read
  only for a repository pinned to its GitHub id (`project repository github`,
  through a connection the repository is delegated to). A disabled repository
  can be pinned: the factory then reads its production state but still takes
  no new work on it.
- **`otlp-remote` (push):** a deployed system exports its own OpenTelemetry
  traces to its factory through the relay, on any platform and any plan. See
  *Remote ingest* below. There is no platform-specific code.
- **Not yet built:** Sentry and PostHog pull adapters. Each is an adapter of
  the same shape, added when a system that needs one is configured. An unused
  adapter is dead code.

Pull adapters are configured in `observe.json` in the factory home:

```json
{"sources": [{"adapter": "cloudflare", "environment": "production",
  "account": "<account id>", "token": "<read-only API token>",
  "services": {"dark-factory-relay": "dark-factory-relay"}}]}
```

- `services` maps a platform name to the unit's `service.name`, and doubles
  as the alias table for correlation.
- The token is an operator-created read-only API token (Account Analytics
  Read). It lives in `observe.json`, inside the owner-only factory home like
  `operator.token`, and is never served.
- factoryd polls each source at most every five minutes, in the background,
  for the last five minutes.
- With no configuration, the remote units truthfully read `unobserved`.

Local processes export with the standard
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://127.0.0.1:43123/v1/traces` (the
browser listener's port), in the SDK's default `http/protobuf` or in
`http/json`. factoryd folds every span into the runtime store and never keeps
it.

Agents' runs export there with no per-project setup: a worker's environment
carries `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` for that listener and
`OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=local`, so the tests,
dev servers and scripts it runs light up the plant if they are instrumented.
An observation's environment is the span resource's
`deployment.environment.name` (or the older `deployment.environment`),
defaulting to `local`; it remains only what that process claimed, and its
source stays `otlp`.

**Agent telemetry.** The same listener takes `POST /v1/metrics` and
`POST /v1/logs` from a worker's own agent CLI, on the same terms as traces.
The worker's `OTEL_RESOURCE_ATTRIBUTES` adds `dark_factory.run.id=<run id>`,
and only an export whose resource names a run factoryd is running (or ended
within the last 15 minutes) is counted; anything else is dropped. Claude Code
is enabled with `CLAUDE_CODE_ENABLE_TELEMETRY=1`, the `otlp` metrics and logs
exporters and the metrics and logs endpoints; Codex through per-run
`-c otel.exporter=…` and `-c otel.metrics_exporter=…` overrides, since only its
configuration turns its exporters on. factoryd keeps, per run and in memory only:
input and output tokens and cost (from `*token*usage*` and `*cost*` metrics,
either temporality), tool calls and API requests (log events whose
`event.name` ends `tool_result` or `api_request`), and the time of the last
one. They ride the console's `RUN_PATHS` answer as `telemetry`; the floor
tooltip and agent panel show only non-zero recorded counts, and a busy worker
whose agent recorded nothing for two minutes is drawn waiting. Prompt text,
tool input and output and every other attribute are discarded while decoding.
Spend is never in the public projection.

### Remote ingest

A remote system's truth comes from the system itself, so the deployed app
exports its own traces to its factory. The factory has no inbound address;
the relay it already dials carries them. This is the one path for every
platform: it needs no platform feature, plan or vendor drain, only an OTel
SDK in the app, which the factory can add itself as an ordinary change.

- **Endpoint.** `POST https://relay.darkfactory.build/ingest/<node id>/v1/traces`
  with `Authorization: Bearer <secret>`, `Content-Type`
  `application/x-protobuf` or `application/json`, optionally
  `Content-Encoding: gzip`. The secret is never in the URL.
- **App setup.** The standard exporter variables, set in the platform's
  production environment:
  `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=<that URL>` and
  `OTEL_EXPORTER_OTLP_TRACES_HEADERS=Authorization=Bearer%20<secret>` (the
  value is URL-encoded), with `service.name` the unit's name. A Next.js app on
  Vercel registers `@vercel/otel` in `instrumentation.ts` with an OTLP
  exporter. Each server invocation exports its own spans; static and
  CDN-cached responses run no code and send none, so they stay unknown, never
  idle.
- **Secret.** The operator asks for one in the console (Settings → Devices &
  pairing → Telemetry ingest).
  factoryd mints 32 random bytes, shows them once, and keeps only their
  SHA-256 in `<home>/ingest.sha256` (0600). Minting again rotates: the old
  secret stops at once. Revoking deletes the file.
- **Relay.** On every relay connection, and whenever the digest changes,
  factoryd sends an `INGEST_KEY` record (0x07, host to relay, connection 0)
  carrying the 32-byte digest, or nothing to revoke. factoryd keeps the
  current digest in memory and queues it first on each new connection. The
  relay keeps it in the host socket's attachment, so it lives exactly as long
  as that socket: no storage, nothing to delete on close, replace or redeploy.
  The Worker refuses, bodiless and before waking any object: anything but
  `POST` to `/v1/traces`, a bearer that is not 43 base64url characters, a
  missing `Content-Length` or one over 1 MiB. The node object then refuses:
  no connected host, or a bearer whose SHA-256 is not the host's digest (401
  either way, so a stranger learns nothing about whether the factory is up);
  an unsupported `Content-Type` or `Content-Encoding` (415); a byte bucket
  that cannot cover the `Content-Length` plus 4 KiB per push (429; 4 MiB of
  burst refilled at 1 MiB a second, in memory per object). Bytes, not requests: an app sends one
  small export per invocation where a collector sends a few large ones.
  Only then is the body read, bounded to 1 MiB, and sent to the host as one
  `INGEST` record (0x08, relay to host, connection 0): one byte of flags
  (protobuf, gzip) and the body. The relay answers 200 `{}` without waiting,
  and stores and logs nothing of it.
- **factoryd.** The connector never fails the relay connection over an
  export. It copies the record and delivers it off the relay reader; exports
  awaiting delivery are bounded at 4 MiB of memory (a gzipped export counts
  as the 1 MiB it may unpack to), past which one is dropped. Delivery posts it to the loopback
  receiver (`/v1/traces`) exactly as a local exporter would, marked remote: the
  same 1 MiB bound after decompression, the same decoding and fold. Remote
  spans are recorded with source `otlp-remote`, and their environment is the
  resource's `deployment.environment.name` or `remote`; a claim of `local` is
  read as `remote`. Only traces are taken: the metrics and logs paths count a
  running agent's own telemetry and nothing remote belongs there.
- **Correlation.** A pushed span lands on the unit whose `service.name` it
  carries, or the one `observe.json`'s `services` alias table names for it.
  Coverage is per `service.name` that sent spans, exactly as for local OTLP.

#### Threat note (remote ingest)

Reviewed adversarially before building; the review moved the rate limit
before the body read and out of the per-location rate-limit binding, the
digest from storage into the socket attachment, and the decode off the relay
reader.

- **Forged telemetry.** Writing into a factory's plant needs its secret,
  256 random bits. A holder of a leaked secret can make that factory's remote
  units, and its public projection in buckets, show activity that did not
  happen, up to the rate. Remote spans carry their own source and never pass
  as local. The operator rotates or revokes in the console.
- **Secret exposure.** The secret is never in a URL. The relay's code never
  logs headers, and Cloudflare's invocation logs were checked for the
  `Authorization` header on the first deploy. It is shown once, over the
  authenticated operator connection (relayed controller frames pass through
  the relay, which already carries them). factoryd and the relay keep only its
  digest; an unreadable `ingest.sha256` counts as revoked.
- **Node id in the URL.** The node id already names `/host` and
  `/controller`. Alone it opens neither: a host needs a token signed by the
  node key, a controller a ticket signed by it. The URL sits in the
  operator's platform settings, not on any public page.
- **Unauthenticated load.** Malformed requests stop in the Worker. A
  well-formed one with the wrong secret costs one object request and is
  refused before its body is read. The bucket is spent only after
  authentication, so strangers cannot spend a factory's budget.
- **Authenticated flood.** 4 MiB of burst, then 1 MiB a second, charged by
  `Content-Length` plus 4 KiB per push (so at most 256 empty pushes a second)
  before any body is read, so concurrent pushes cannot pile
  bodies into the object's memory. factoryd delivers off the relay reader with
  at most 4 MiB pending, gzipped exports counted at their 1 MiB unpacked
  bound, so controllers are never stalled and a bad export
  never drops the connection.
- **Decoder exposure.** The OTLP decoder was reachable only from local
  processes. It is now reachable by a secret holder. It is memory-safe Go,
  bounded at 1 MiB after decompression, and every span, with or without a
  `service.name`, counts toward the per-export observation cap.
- **Stale or revoked secrets.** A digest lives only in its socket's
  attachment, and each connection starts with the current one. A secret
  revoked while offline never delivers.
- **Storage at rest.** None. The digest is socket state; pushes are in flight
  only.

## 7. Correlation

Correlation runs for each observation:

1. **Exact.** The unit matches, through `service.name`, the platform's
   script or project name, or an alias. Every node selector also matches
   after normalisation:
   - Parameters become `{}`: `{id}`, `:id`, `[id]` and `<int:pk>`.
   - Catch-alls become `{*}`: `[...x]`, `[[...x]]`, `*` and Go `{x...}`.
   - `(.:format)` and trailing slashes are dropped.
   - Methods are compared case-insensitively. `ANY` and an empty method match
     every method, and `HEAD` matches `GET`.

   A concrete path matches the template with the most static segments. A tie
   leaves the observation unbound. An HTTP request never binds to the bare
   listener it arrived on, so an unmatched route stays visible as unknown.
   Methods on a socket whose code names none are that socket's work.
2. **No unit.** An observation whose `service.name` names no unit of this
   project (and no alias) is not this project's. Runtime evidence is
   factory-wide, so it may belong to another project. It never binds into a
   unit and never enters this project's quarantine. A name that two units
   answer to binds nowhere at all.
   Client and producer spans bind to the party or the ingress they name:
   - a shared store or queue by its selectors;
   - another unit's ingress by its declared host or loopback listener;
   - otherwise a runtime-only external party.
3. **Unmatched.** A runtime-only node of kind `unknown` is created under the
   observed unit.
   - It is keyed by method and first path segment (or `rpc.method`,
     destination or host), never the full concrete path.
   - There are at most 32 per unit. The rest fold into one "other" node with a
     count.
   - It is shown and queryable, and it is a candidate for investigation.

Runtime evidence is factory-wide and keyed by service name. Two projects
whose units share a `service.name` see the same evidence. `observe.json`
aliases are the way to tell them apart. This is a known limit, not a guarantee.

A unit's identity includes the declaration that found it. Adding a compose
file to a Django repository therefore re-identifies its unit, from
`django:.` to `compose:web`, along with everything inside it.

The inverse lists are also queryable:

- static nodes that a covering source has never observed
- runtime-only nodes
- contradicted nodes

Nothing is hidden or silently rounded into a neighbour.

## 8. Observation coverage

Each node has an **observation state**, separate from its **operational
state**.

A source **covers** a node only when two things hold:

1. It covers the node's unit.
2. Its granularity includes every selector key the node binds on.

Cloudflare's per-Worker analytics therefore cover the Worker's processor
node and nothing inside it.

| Observation state | Meaning |
| --- | --- |
| `observed` | A covering source saw activity within the window. |
| `quiet` | A covering source is current and saw nothing in the window, and the source has bound traffic to this node before. Silence is not claimed for a static guess the source has never matched: a route whose prefix the code hid stays `partial`. A timer is never `quiet`, because it can be silent for longer than any window held. |
| `partial` | Only a coarser source sees it. For a leaf, the unit is covered but the node is not, so the unit's traffic may or may not pass through it. For a processor, some of its nodes are covered and some are not. |
| `stale` | It was covered, but the source has not reported within its TTL. |
| `unobserved` | No source can see it. |
| `opaque` | An external party. Calls to it may be observed from the caller's side, but its own state never is. |

| Operational state | Allowed when |
| --- | --- |
| `active`, `degraded` (errors ≥ 5%), `failing` (errors ≥ 50%) | `observed`, a `partial` processor with its own counts, or `opaque` with caller-side counts |
| `idle` | `quiet` only |
| `unknown` | everything else |

Static evidence never produces an operational state. Tests in
`internal/opgraph` assert that only `quiet` is ever `idle`. A further test
covers a unit that is observed while its child is unbound: the child is not
idle.

Edges follow the same rule. An edge is observed only through a source that
reports peers, such as `factoryd`'s outbound calls or client spans. Otherwise
it is `unobserved`, not empty.

The snapshot carries a coverage summary for the UI's header:

```text
37 operational components inferred · 21 observed · 6 partial · 10 unobserved
```

## 9. Layers

```text
repositories ── static extractors ─┐
                                   ├─> Operational Graph (internal/opgraph)
adapters ─> observations ─ correlate ┘      + runtime state + coverage
                                              │   OPERATIONAL_GRAPH frame
browser:  graph + work state (tasks, runs, run paths, proposals)
            └─> world.ts   (units, stations, flows, worker targets)
                  └─> layout.ts / factory-scene.tsx / movement / idle life
```

- Inference changes never touch the browser.
- A new adapter changes neither the graph types nor the browser.
- The world projection is the only place where semantics become places.
- The renderer reads only the world.
- Public projection is a server-side allowlist over the same graph and work
  state (section 13).

## 10. Visual language

The plant is read left to right: arrivals, processing, storage, departures.
Motion and physical state carry information. Decoration never stands in for
data.

| Meaning | Picture |
| --- | --- |
| Deployment unit | Its **main line** (a press or assembler, named for the unit) and the machines it owns, standing together on the one shared floor over a faint tint: its **area**. An area is paint, not a wall or a door; it may come in several lobes. The unit's runtime is said in its tooltip and inspector. Selecting the main line lights every machine the unit owns and every belt touching them. |
| Ingress | An **intake dock** beside its unit's main line. More than six routes fold into one manifold with a count; zoomed in to twice the fitted scale or more, the manifold lists its routes inside its own footprint. |
| Job | A **cell** with an arm, standing on a base; its belt rises from the main line into the base. |
| Timer ingress | A **clock** beside its unit, sending a pulse. |
| Queue | An **accumulation conveyor**, one machine however many units use it, standing near them on neutral floor. Backlog piles up visibly on it. |
| Store | A **silo**, tank or rack store. Reads and writes are pipe pulses. |
| External | A **gate** to the outside near whoever calls it, labelled with how much of it can be seen; opaque parties sit behind fog-hatched gates. |
| Unknown | A crate marked `?`, on neutral floor near the unit it claims, if any; never inside an area. |

| Physical state | Picture |
| --- | --- |
| Throughput | Item density on belts: a log scale with fixed speed, so density reads as rate. |
| Latency | Dwell: items wait visibly inside a machine's window. |
| Errors | Items drop into a scrap bin, which fills with error rate. The andon lamp turns amber or red. |
| Retries | Items loop back on a recirculation belt. |
| Deploy | A muted "deployed" tag on the unit's main machine for 24 hours; the tooltip gives the time. |
| Dormant | **Observed quiet**: the machine is solid and lit, but still. |
| Unobserved | **Greyed machine**: built but translucent and colourless, an unknown (grey-blue) lamp, plate "no telemetry", no material. Never drawn as an idle machine, whose lamp is dark. |
| Partial | Solid machine with a half-lit lamp; the tooltip says "partly observed". |
| Stale | Desaturated, with a "last seen" tag. |
| Inferred-only edge | Faint grey dashed belt with no material. |
| Observed edge | Solid belt carrying material. |
| Runtime-only edge | Solid belt with a `?` tag. |
| Contradicted | A red tag. |

Alternatives considered and set aside:

- One machine per node arranged as a graph: an architecture diagram with
  sprites.
- Repository rooms: they repeat the old error.
- City or blocks: no material, so throughput and latency cannot be seen.
- Pipes only: it reads as a plumbing diagram, and workers have nowhere to
  stand.

The plant metaphor wins because material can wait, pile up, scrap and loop,
and those are exactly latency, backlog, errors and retries.

## 11. Level of detail and scale

There is one detailed floor, and the viewer zooms and scrolls it. Detail
never depends on data:

- **Overview** (zoomed past fit only): a minimap of the whole plant in the
  floor's corner, a quarter of the pane on a phone. It shows machines
  coloured by coverage, areas, workers and the visible window; clicking or
  dragging it moves the floor. It folds to a button, and the browser
  remembers that.
- **Floor** (zoomable): one station per node. Ingress beyond 6 per unit always
  folds into a manifold. Fit shows the whole floor, facilities and externals
  included, by the pane's width and height; the pane's size comes from CSS
  and is watched, never the floor. Zoom
  (buttons, or ctrl/pinch and the wheel) scales the floor without moving
  anything on it. At twice the fitted scale or more, a manifold lists its
  routes by label inside its own footprint; there is no detail setting.
- **Station** (on selection): the inspector shows selectors, evidence and
  sources.

The `OPERATIONAL_GRAPH` frame carries for each node only:

- ID, kind, label and unit
- up to two source paths
- the three states and a rate bucket

Selectors, evidence and the full source list come from
`OPERATIONAL_NODE_GET` when the inspector opens. This keeps 4,096 nodes inside
the 1 MiB frame.

### The connected floor

There are no halls. The floor is one connected space: machines are placed
first, at their drawn sizes, and everything else (areas, aisles, belts,
walks) is derived from where they stand. Three things stay separate:

- the **software graph**: units, operations, ownership, flows, evidence;
- the **physical layout**: footprints, interaction positions, belt routes and
  the free floor workers walk on;
- **areas**: one faint region per neighbourhood of a unit's machines,
  painted after layout and never read by it.

Approach (`factory-scene/scene.ts`, `factory-scene/movement.ts`; nothing in
`web/` offered graph layout or geometry, so no dependency was added):

1. **Footprints.** Every station's footprint is its body, its label and the
   spot a worker stands at to use it, grown by a margin on every side.
   Footprints never overlap; the margins of two neighbours make an aisle wide
   enough for a worker, so every interaction position is reachable by
   construction.
2. **Placement.** Stations are linked by ownership (a member to its unit's
   main machine, weighted most) and by flows; a runtime-only node is linked
   only to the unit the graph says it claims. Linked stations form connected
   groups. Within a group, the most linked station goes first, then the
   station most linked to those already placed (ties by ID). Each is tried
   against every side and corner of every placed neighbour and takes the
   free spot with the shortest weighted distance to its neighbours plus a
   charge for growing the group's bounds; if none is free it takes the
   nearest free spot on a spiral. Two bounded passes then move each station
   to a better free spot if one exists. Placement has a work cap in
   proportion to the links it must honour (4,000 tries plus 200 a link end;
   ordinary groups, a store shared by forty services included, use a fifth
   of it). Only a runaway group, such as thousands of machines all linked
   to one hub, passes it; that group, or that comes out longer than 2.2 times its width either way (a
   chain), is laid instead in rows near 16:10 in placement order, each row
   running back the way the last came, so neighbours stay side by side.
   Then the groups are packed on
   shelves, tallest first, with a gap that keeps unrelated groups visibly
   apart; the shelf width is the one that keeps the floor smallest and
   nearest 16:10 (chains of unrelated units do not make a strip). The
   development block (the commons, its work tables and the outbound line)
   goes last, at the bottom, at a fixed width. Only IDs, kinds, labels,
   ownership and flows enter; traffic, state, workers, the agent count and
   the pane never do, so input order, a busy hour, zoom or a resize cannot
   move anything.
3. **Stability.** Given the previous layout, a structural change keeps every
   station that still exists where it was, places new ones beside their
   placed neighbours (or beside the floor for a new group), and leaves the
   gaps that removals open. A kept station that has grown into a neighbour
   (a longer label) is placed afresh beside its neighbours. When more than a
   quarter of the stations would be placed afresh, the floor is laid out cold
   again. Nothing is stored: a reload lays the same structure out exactly
   as before, and only a structural change between sessions (machines,
   flows or ownership) can rebuild it. The agent count only furnishes the
   development block, which grows downward from where it stands, below
   everything; nothing new is ever placed under it.
4. **Walking.** One geometry serves placement, solids, interaction positions
   and navigation. Machine bodies, the commons tables and the break-room
   implements are solid; labels, belts, area tints and the work line are
   floor markings. Workers walk an occupancy grid (6 px cells; a cell is
   free when a worker anywhere in it clears every solid) with
   diagonal steps that never cut a corner, then the path is pulled straight
   wherever the exact segment clears every solid by a worker's radius. A
   seat or a standing place inside a table's or machine's clearance is
   reached by one short step from the nearest free cell; nobody walks the
   length of a table past the people at it. A walk always
   starts from where the worker is drawn. If no route exists the worker is
   put at its destination, marked as not walked, and never drawn through a
   machine. Execution never waits for a sprite.
5. **Belts.** Belts run on their own 8 px grid, square. They keep off
   machine bodies, every label, the spots workers stand at and the
   development block, and they leave a machine only from its sides or its
   top, never its front. They are laid shortest first; each takes the
   facing ports and the run cheapest in length, bends and crossings. When
   every port of a machine is taken, a belt joins a belt already serving it
   square-on, at a junction dot. For every routed belt these hold: it is
   square; it never runs along another belt (belts between the same places
   lie a cell apart, as a bundle); it crosses another only straight
   through, at a right angle, never where the other turns and never within
   10 px of a label, a port, a junction or a machine; and every crossing is
   drawn as a bridge (a deck and two rails), unlike a junction's dot.
   Each run is searched only in a window round its two ends and its work is
   bounded by its length. Belt searches share one budget and link searches
   another, each 150,000 steps plus 200 a connection, so all the work grows
   linearly with the number of connections. A connection with no belt route
   inside its budget is an **overhead link**: a thin, faint, dashed line,
   square, laid round machine bodies and labels only. It never passes
   through a machine or a label, but it may cross or follow belts and other
   links and carries no bridges or material; the crossing rules above are
   for belts alone. Past the link budget too, a connection is drawn as a
   short dashed stub out of a free port of its own at each end. Each stub
   names the machine at the other end where the name touches no machine,
   label, belt, link or other name (a few spots by its end, then a shorter
   name); otherwise the name is the stub's title. A machine with no port
   left shows no stub, and the other end still names it. No connection is
   ever dropped.
6. **Areas.** A unit's members whose footprints face each other, side to
   side or corner to corner, across no more than two aisles merge into one
   irregular region, the gap
   bridged only where no other machine is in the way; members further apart
   are separate lobes. A region covers only its members' footprints and the
   gaps between them, never another machine. Each region is named once, at
   its top left, in floor its own footprints leave clear, and neighbouring
   areas never share a hue while the palette of eight allows.

**The development neighbourhood** is the commons (break room and work
tables) with the outbound line right below it. Its size comes from its
contents: a seat and a table place for every agent at rest, the work tables
for half of them, four to a table, and only the implements the floor offers
(board, missions, tasks, library, and coffee with scenery on). It is
always as wide, and sits at the bottom of the floor, so a different agent
count only makes it taller, downward, and never moves a machine or a belt.
Which agents are busy never changes it.
Anyone beyond its seats (a reviewer passing through) sits on a bench below
the floor.

Trade-offs: the greedy placement is not an optimiser. It keeps related
machines close and the floor compact on the fixtures, but a long chain or a
unit used by many others still gets some long belts, which are drawn rather
than hidden by duplicating machines. Square belts that keep off labels and
standing spots are longer than straight ones (about 1.6 to 1.9 times on the
fixtures), and on a dense floor they fill the aisles like a circuit board.
Laying belts costs up to about half a second on the largest fixture, once
per structural change. Kept positions after a change can leave
holes until the next cold layout, and a new machine whose neighbours are
surrounded takes the nearest free floor, which can be a few machines away.
Large, highly connected graphs make the group nearly square, so a very wide
or very tall pane leaves margin. On dense random floors with long flows
across the whole floor, about half the connections become overhead links.
Rendering a floor (layout, areas and belts, measured with
`renderToStaticMarkup`) takes about 0.1 s at 200 nodes, 0.3 s at 1,000 and
1 s at 4,100 nodes with 3,900 edges on a laptop; at the documented bound,
4,000 machines all flowing to one hub take about 1.3 s, and 800 units
sharing one store about 1.1 s, most of their connections then being links
or stubs. It runs only when the structure changes; a reading, a worker or
the agent count never reruns it.

Bounds: 4,096 nodes and 4,096 edges served.

## 12. Workers

The plant is what the software does. Workers are what agents do to it.

- A running task's `RUN_PATHS` sample maps to nodes whose `sources` contain
  the paths (longest prefix). With no node match it falls back to the unit
  whose source root contains them. The worker walks to that machine and
  works there.
- With no sample, the worker waits at the planning tables. With no task, the
  worker rests in the commons, or beside its unit's main line when rest is
  set to nearby.
- Reviewers carry a clipboard at the changed machines. CI and checks show on
  the outbound line's Checks station while the Change's checks run. A merge followed by a
  deploy observation for that unit plays the changeover.
- A machine whose state is `failing`, or an unexplained crate, is
  inspectable. The inspector's **Investigate** action creates an ordinary
  task. The task carries only the node ID, kind, static label, source paths
  and counts. Runtime-only selector values come from unauthenticated local
  senders, so they never enter agent instructions. Investigation, repair, review, CI,
  deploy and recovery are then the real pipeline, shown by the same visuals.
- Ambient life stays: the cat, coffee, chatter and errands. It is labelled
  ambient and never invents operational events.

## 13. Public and private projections

One state, two projections, both computed by factoryd:

- **Operator** (paired browser): everything above.
- **Public** (`dark-factory-site`): an allowlist built by
  `opgraph.Public`.

`opgraph.Public` contains only:

- node kind, an anonymous stable ID (`HMAC(project secret, id)`), and an
  ordinal label in plain words, counted per word: a unit by its runtime
  ("Web app", "Web server", "Edge function", "Service", "Tool",
  "CI pipeline"), an ingress by its trigger ("Entrance", "Timer",
  "Inbox"), a job by its unit ("Check" in CI, else "Loop", short enough
  to fit its station), then "Store", "Queue", "Outside service" and "Unknown"
- unit grouping, edges and their evidence state
- coverage and operational state
- bucketed activity: none, low, medium or high, from log₂ of rate

Workers appear as an anonymous count by activity and location (unit), with
no names or task text. Deploy and changeover events carry no versions.

The outbound work line appears as crates: each open pull request, and each
merged one for a day, as an anonymous stable ID (`HMAC(secret, "crate:" +
repository#number)`), its station (review, checks, merge queue, shipped) and
a fault flag, never a title, number or branch. factoryd places them by the
console's rule (`publicCrates`, `projectCrates`; one shared fixture,
`web/fixtures/production-crates.json`, holds both to it) on its own clock.
*SHIPPED* means deployed where the repository has GitHub deployment records
of its production environment: a merged crate ships once a successful one
was created at or after its merge, and waits at the merge queue's end until
then. The repository record keeps the newest such time once seen, so a failed
read moves nothing. A repository whose last production deployment is more than
seven days before a merge no longer deploys that way, so that merge ships: a
repository that changes how it deploys never holds its line. Without
deployment records, SHIPPED means merged.

A `ledger` is added only for repositories GitHub serves to an anonymous
reader: factoryd reads each repository's releases without credentials once
an hour, and only a 200 makes it public; anything else, or no answer yet,
is private and adds nothing. The ledger carries what those public
repositories already show: every open pull request, every accepted issue
not yet finished, merges in the last 7 days and a 21-day hourly clock of
them from production and intake records, and the releases published in
those 21 days, paged from the same anonymous read. Past the
relay's bound the oldest listed merges, then open pull requests, give way;
their counts stay.

It never contains:

- labels or paths from source
- selectors, hostnames, routes or secrets
- binding names
- issue or PR text, errors or terminal output

Ordinal labels are assigned in HMAC-ID order, so they do not shift as other
nodes come and go.

factoryd serves the projection to local readers at
`GET http://127.0.0.1:43123/v1/public/<project id>`. It refuses any request
carrying an `Origin`, and any whose `Host` is not the listener itself (DNS
rebinding). Public identities are keyed by a 32-byte secret,
`public.key` (mode 0600), which never leaves the home.

### Live feed

Publishing is opt-in per factory. Nothing leaves the machine until the
operator names one project in `observe.json`:

```json
{ "public_project": "<project id>", "sources": [] }
```

factoryd then sends that project's PublicWorld bytes, exactly what
`/v1/public` serves, over its existing relay connection as a `PUBLISH`
record. It sends at most once a minute and only when the bytes changed.
`generated_at` moves every five minutes, so a running factory publishes at
least that often. On start it logs the public id it publishes under. The
relay keeps only the latest world per factory and serves it read-only at
`GET https://relay.darkfactory.build/public/<public id>`.
`Last-Modified` is when the relay received it. The site polls that URL on
`/log`, falls back to its committed snapshot, and reads a feed older than
seven minutes as not live.

The public id is `base32(SHA-256("dark-factory-relay/public\n" ‖ node public
key)[:20])`. It has the node id's shape but cannot be turned back into it,
and the node key never leaves the relay and the home. A reader of the site
learns nothing it could use to dial `/host` or `/controller`.

### Threat note

- **Spoofing another factory's world.** A world is stored under the public
  id derived from the key whose host token the node's Durable Object
  verified. The relay never accepts an id from the publisher, and the
  public object takes writes only from that node object. The Worker
  forwards only `GET` to it. Claiming another factory's id needs its node
  key.
- **Oversized or abusive publishes.** A world over 128 KiB, one that is not
  a JSON object with a numeric `generated_at`, or one sooner than 30 s after
  the last is dropped. It costs one record on an already authenticated
  socket and only ever replaces the sender's own single record. Storage is
  O(1) per factory, with no history and no listing endpoint.
- **Leaking private data.** The publisher has one source:
  `browserBackend.PublicWorld`, the `/v1/public` handler, which marshals
  `opgraph.Public`. Its allowlist is enforced by its output type, not by
  redaction. The bytes go to the relay unchanged, and the relay stores them
  verbatim and adds nothing. One thing is new: `Last-Modified` tells a
  reader, to the minute, when the public world last changed. The world
  itself still holds only bucketed activity.
- **Cost of public reads.** Each read is one Worker request and one Durable
  Object read. Reads are rate limited per client address (60 a minute), and
  `Cache-Control: public, max-age=15` covers repeat polls. CORS admits only
  `https://www.darkfactory.build`, but CORS is not access control: the
  world is public by design. If reads ever cost real money, put
  `caches.default` in front of the object.
- **Stopping.** Remove `public_project` from `observe.json`. Within a minute
  factoryd publishes an empty world, and the relay deletes the stored one.
  Every start without the setting does the same once, so a world left by an
  earlier run is retracted too. Stopping the relay (`--relay-origin` unset)
  stops publishing as well. The last world then stays as "last seen" until
  a later start retracts it.

What remains visible is the shape (node and edge counts), bucketed activity,
unit-level worker presence and, for a live feed, when it last changed. That is acceptable for public repositories,
and it is stated on the page.

## 14. Security and privacy

- **Tokens.** Adapter tokens are read from operator-owned files named in
  `observe.json`, held in daemon memory, and never served. Adapters only
  read.
- **OTLP receiver.** Loopback only. It accepts no browser `Origin` and only
  `application/json` or `application/x-protobuf`, up to 1 MiB after
  decompression, and aggregates without retaining spans, metrics or log
  records. Agent telemetry keeps counts and cost only.
  Attribute values are length-bounded, and only the selector keys listed in
  section 4 are kept, so arbitrary payload data never reaches the graph.
- **Static extraction.** Reads exact-revision archives (`BuildArchive`) and
  never executes project code or external analysers. URL literals keep scheme, host and path template only, never query
  strings or credentials.
- **Public projection.** An allowlist, never redaction. A test serialises a
  projection built from a graph seeded with canary secrets in every field and
  asserts that none appear.
- **Operator frame.** Bounded by the existing 1 MiB frame. Node detail
  carries source paths and selectors (operator-visible today), never source
  text.

## 15. Adversarial review

The design and each implementation PR are attacked by independent reviewers.
The attacks cover:

- generalisation beyond Dark Factory
- multi-repository handling
- inference accuracy
- correlation
- coverage semantics
- browser visibility
- identifier stability
- graph size
- layout stability
- telemetry coupling
- public leaks
- excess abstraction
- rendering concepts leaking into semantics
- retained legacy

They must also answer one question: is there a materially simpler
architecture with the same outcome? Findings and resolutions are recorded on
the pull requests.

A failed merge-queue run on a factory-reviewed branch is repaired by the
factory itself: it pushes a fix commit carrying `Dark-Factory-Operation` to
that branch. A human pushing to the same branch must fetch first.
