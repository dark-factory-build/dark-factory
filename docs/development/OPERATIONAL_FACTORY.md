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
| Break room, errands, commons, Missions/Tasks/Library stations | `runFootprint`: changed paths now map to operational nodes | Name-based `responsibility()` motifs, file-count `equipmentScale` |
| Reviewer pseudo-workers, change marks (`ProposalMark`) | Proposal relationships: import deltas give way to changed operational nodes | Inventory inspector, `source_files`, the "detail" grouping setting |
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
| ast-grep, Tree-sitter | Not adopted. A tool that is optional on `PATH` would make the node set, and so the IDs and layout, depend on the machine. Cgo bindings break the pure-Go build. Route and call patterns in TypeScript, Python, Ruby, Java and Rust are instead a small table of `inferred` regular expressions. The upgrade path is ast-grep rules with the same table shape, if precision ever needs it. |
| SCIP (scip-go, scip-typescript), gopls, tsserver | Not adopted. Each needs a build or type check. They provide precise references, which an operational graph does not need. Symbol strings embed versions, so they are unstable as identities. |
| CodeQL | Rejected. Its licence restricts it to open-source codebases, and it builds a database in minutes, not milliseconds. |
| Semgrep CE / Opengrep | Rejected. It is a heavy runtime, and Semgrep's maintained rules carry a restrictive licence. The pattern table covers the same cases. |
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
- extractor or source: `go-ast`, `wrangler`, `nextjs`, `otlp`, `cloudflare`
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
Repository does not decide placement: halls are deployment units, and one
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
  - `urls.py` `path()` entries become ingress at `inferred` confidence
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
  source        adapter id ("factoryd", "otlp", "cloudflare", "vercel", "github")
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
| `vercel` | `service.name`, method, path |
| `factoryd` | `service.name`, `rpc.method`, `url.path`, `process.executable.name`, `server.address` |
| `otlp` | whatever keys a span carried for that service |

**Adapters, in order of yield:**

- **`factoryd`**: the daemon observes its own listeners: browser messages,
  HTTP requests and local API operations, counted with `rpc.method`,
  `url.path` and `network.transport`. Its outgoing work (provider launches,
  GitHub calls) is not observed yet. Coverage is local and current while the
  daemon runs.
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
- **Not yet built:** `vercel` (runtime logs), `github` (workflow runs and
  deployments), Sentry and PostHog. Each is an adapter of the same shape. They
  are added when a system that needs one is configured. An unused adapter is
  dead code.

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
- With no configuration, the remote halls truthfully read `unobserved`.

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
   hall and never enters this project's quarantine. A name that two units
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
            └─> world.ts   (halls, stations, flows, worker targets)
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
| Deployment unit | A **hall**: a walled production building with the unit's name plate. Repository is a coloured tag on the plate, not a wall. |
| Ingress | An **intake dock** on the hall's left wall. Arriving material comes in from the yard. More than six routes fold into one manifold with a count. |
| Processor work | The hall's **main line**: a press or assembler. |
| Job | A **cell** with an arm. |
| Timer ingress | A **clock** on the hall wall, sending a pulse. |
| Queue | An **accumulation conveyor** between halls. Backlog piles up visibly on it. |
| Store | A **silo**, tank or rack store. Reads and writes are pipe pulses. |
| External | Gates in the **perimeter fence**, with lorries or pipes passing through. Opaque parties sit behind fog-hatched gates. |
| Unknown | A **quarantine bay** of crates marked `?`. |

| Physical state | Picture |
| --- | --- |
| Throughput | Item density on belts: a log scale with fixed speed, so density reads as rate. |
| Latency | Dwell: items wait visibly inside a machine's window. |
| Errors | Items drop into a scrap bin, which fills with error rate. The andon lamp turns amber or red. |
| Retries | Items loop back on a recirculation belt. |
| Deploy | Changeover: scaffold and a version plate flip. Change marks remain for 24 hours. |
| Dormant | **Observed quiet**: the machine is solid and lit, but still. |
| Unobserved | **Greyed machine**: built but translucent and colourless, an unknown (grey-blue) lamp, plate "no telemetry", no material. Never drawn as an idle machine, whose lamp is dark. |
| Partial | Solid machine with a blueprint quarter and a half-lit lamp. |
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

- **Overview** (always visible): a minimap of the whole plant in the floor's
  corner. It shows halls coloured by coverage, gates, workers and the visible
  window. Clicking or dragging it moves the floor.
- **Floor** (zoomable): one station per node. Ingress beyond 6 per hall folds
  into a manifold unless the viewer picks "every route", and the quarantine
  bay aggregates unknowns. Zoom (buttons, or ctrl/pinch and the wheel) scales
  the floor without moving anything on it.
- **Station** (on selection): the inspector shows selectors, evidence and
  sources.

The `OPERATIONAL_GRAPH` frame carries for each node only:

- ID, kind, label and unit
- up to two source paths
- the three states and a rate bucket

Selectors, evidence and the full source list come from
`OPERATIONAL_NODE_GET` when the inspector opens. This keeps 4,096 nodes inside
the 1 MiB frame.

Layout is deterministic and stable:

1. Halls sit in fixed bands by runtime: browser units, then edge and server
   units (Workers, web servers), then long-running processes, then
   command-line tools. Within a band they are ordered by ID. Adding an edge
   never reorders halls. A hall's width grows with its static machines, so a
   new route can move later halls along its band; it never moves them
   between bands.
2. Within a hall, stations sit in fixed zones by kind.
3. A shared store or queue sits in the yard beside the hall of its lowest-ID
   user.
4. External gates follow the fence in ID order.
5. Runtime-only nodes go only into the quarantine bay, so live data never
   moves static machines.

Bounds: 4,096 nodes and 4,096 edges served; 64 halls drawn, with the rest
folded into "N more units".

## 12. Workers

The plant is what the software does. Workers are what agents do to it.

- A running task's `RUN_PATHS` sample maps to nodes whose `sources` contain
  the paths (longest prefix). With no node match it falls back to the unit
  whose source root contains them. The worker walks to that machine and
  works there.
- With no sample, the worker waits in the hall's office. With no task, the
  worker rests.
- Reviewers carry a clipboard at the changed machines. CI and checks show on
  the hall's test rig while the Change's checks run. A merge followed by a
  deploy observation for that unit plays the changeover.
- A machine whose state is `failing`, or a quarantine bay with unknowns, is
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
  ordinal label ("Intake 3")
- hall grouping, edges and their evidence state
- coverage and operational state
- bucketed activity: none, low, medium or high, from log₂ of rate

Workers appear as an anonymous count by activity and location (hall), with
no names or task text. Deploy and changeover events carry no versions.

It never contains:

- labels or paths from source
- selectors, hostnames, routes or secrets
- binding names
- issue or PR text, errors or terminal output

Ordinal labels are assigned in HMAC-ID order, so they do not shift as other
nodes come and go.

The relay is not the publishing path. It forwards opaque frames, stores only
its host record, and its node ID is the pairing identity.

Instead, factoryd serves the projection to local readers only, at
`GET http://127.0.0.1:43123/v1/public/<project id>`. It refuses any request
carrying an `Origin`, and any whose `Host` is not the listener itself (DNS
rebinding). Public identities are keyed by a 32-byte secret,
`public.key` (mode 0600), which never leaves the home.

Publishing is an explicit operator action, so nothing leaves the machine by
default. The operator saves that JSON into the site repository, and the site
renders it with the same world and renderer in read-only mode.

What remains visible is the shape (node and edge counts), bucketed activity
and hall-level worker presence. That is acceptable for public repositories,
and it is stated on the page.

## 14. Security and privacy

- **Tokens.** Adapter tokens are read from operator-owned files named in
  `observe.json`, held in daemon memory, and never served. Adapters only
  read.
- **OTLP receiver.** Loopback only. It accepts no browser `Origin` and only
  `application/json`, up to 1 MiB, and aggregates without retaining spans.
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
