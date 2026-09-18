# Maintainer GitHub connections

The existing Maintainer Worker and GitHub App own customer authorization. This
adds neither an App registration nor a hosted account service. The host stores
one broker credential through its private credential mechanism; GitHub user,
refresh, installation tokens and the App private key stay inside the broker.

## Browser and host protocol

All paths below share the existing broker origin. JSON replies use `no-store`.
The CLI and console call the private local operator API; the daemon calls these
broker endpoints. Only initiation and callback are unauthenticated. The host
credential is never a URL parameter, browser cookie, snapshot, or MCP argument.

| Request | Reply / effect |
| --- | --- |
| `POST /v1/github/connections` with `{}` | 201 `{connection_id, credential, authorization_url, expires_at}`. Persist the credential privately before opening the URL. |
| `GET /v1/github/connections/callback?state=…&code=…` | Browser-only GitHub callback; consumes state and displays a return-to-host message. No credential is returned. |
| `GET /v1/github/connections/{id}` | `{connection_id, state, github_user?, repositories}`; state is `pending`, `connected`, or `disconnected`. |
| `GET …/{id}/installations?page=1` | `{installations, next_page}`; follow `next_page` even when the filtered page is empty. |
| `GET …/{id}/repositories?installation_id=7&page=1` | `{repositories, next_page}` with numeric IDs, names and current user permissions. |
| `PUT …/{id}/repositories` | `{repositories:[{installation_id,repository_id,repository}]}` replaces the delegation after live validation of every entry; at most 100 repositories. |
| `DELETE …/{id}` | `{state:"disconnected"}`; erases broker-held GitHub authorization and delegation. |
| `POST …/{id}/mcp` | Existing typed MCP JSON-RPC contract, scoped to this connection. `observe_operation` additionally requires `repository`; its customer tool schema advertises this field. |

Every request except initiation/callback requires `Authorization: Bearer
<credential>`. The 256-bit random credential's SHA-256 digest is the connection
ID; the broker persists only that digest. Unavailable or partially configured
customer authorization has no routes. Failure never tries the Access owner or
headless service identity. The existing Access-protected `/mcp` remains the
legacy operator path.

The browser flow uses GitHub App authorization code and S256 PKCE. State is
random, expires after ten minutes, belongs to the initiating host connection,
and is consumed durably before code exchange. Callback replay and changed state
are rejected. Expiring user authorization is mandatory. The broker rotates
refresh credentials before access-token expiry and verifies the numeric GitHub
user ID on every authenticated request; an error fails closed. Disconnect is
irreversible for that connection; a fresh connection is a different receipt
owner, including when the same GitHub user authorizes it.

GitHub's native installation settings remain the repository-access editor.
Discovery and access checks paginate both user installations and installation
repositories. Only active selected-repository installations of this App are
eligible. Every remote operation rechecks the current user, installation,
repository ID and user permissions. Mutations additionally require the user's
push, maintain or admin grant. Read visibility alone never permits publication.
A cross-repository publication also requires live read access and delegation to
its source. The installation token still receives each operation's specific
permission set; user authorization is an additional ceiling.

## Receipt ownership and concurrency

A receipt retains its original UUID. First use binds an immutable connection
owner and the server-verified `github:<repository_id>` scope. Updating a
repository's delegated name after a GitHub rename preserves receipt access.
Legacy receipts remain owned by the original Access operator. Neither another
connection at the same repository nor the legacy operator can claim or observe
a customer's UUID. Referenced reviews, workflow observations and reconciliation
use the same ownership boundary. Completed replies pass live authorization
before the journal can return cached bytes.

One connection Durable Object serializes its requests, including GitHub I/O.
This keeps rotating refresh credentials, delegation edits, disconnect and
publication ordered. A disconnect takes effect after an already running
operation finishes; subsequent requests cannot replay its result. No GitHub
grant cache exists. Installations/repositories beyond 100,000 entries fail
closed during live authorization rather than silently proving a partial list.

Once customer receipts exist, **never roll the Worker back to a build predating
receipt ownership**: an old reader does not know those UUIDs have customer
owners. Recover by deploying a reviewed ownership-aware build. Disabling
customer routes or removing OAuth secrets does not make an older legacy reader
safe. Preserve the Durable Object data independently of code rollback.

## Activation prerequisites and proof

The customer routes are optional and require these three platform secrets in
addition to the existing complete Maintainer authority:

- `DARK_FACTORY_MAINTAINER_CLIENT_ID`: this existing GitHub App's client ID;
- `DARK_FACTORY_MAINTAINER_CLIENT_SECRET`: its OAuth client secret;
- `DARK_FACTORY_MAINTAINER_CALLBACK_URL`: exact HTTPS broker URL ending in
  `/v1/github/connections/callback`, also registered in GitHub App settings.

Enable expiring user access tokens for that same App. Stage secrets without
activating traffic, through the documented reviewed Worker deployment path.
No secret value belongs in the repository, host configuration, provider prompt
or deployment evidence. Repository selection and GitHub user consent remain
native GitHub actions.

The customer path must be reachable by non-owner users without the existing
email-only Access gate redirecting their daemon to a login page. Inspect the
live Access application and policies with the appropriate account authority;
keep the current `/mcp` operator policy intact, and route
`/v1/github/connections/*` to the same Worker. On that namespace, the Worker is
the bearer-authentication boundary. Only initiation and callback accept an
unauthenticated request; this is not an anonymous MCP deployment. Apply the
platform's ingress rate limits to anonymous initiation. Do not claim this
activation from source configuration or a DNS-only token's inconclusive Access
application listing.

Local proof runs `control-plane/scripts/local-ci.sh`. Its workerd fixture uses
a disposable persisted home and fake outbound GitHub responses, but real Worker
code and SQLite Durable Objects. It covers two principals, empty filtered first
pages, second-page repositories, callback replay, concurrent refresh, read-only
publication refusal, cross-owner replay/observation, referenced receipt refusal,
permission loss, installation removal, OAuth revocation and disconnect. Native
SQLite tests additionally prove old UUID ownership and concurrent first claim.
This is fixture isolation proof, not a live second-user authorization result.

Activation evidence must separately record the exact Worker revision, callback
registration, expiring-token setting, optional bindings present (not their
values), Access routing, unauthenticated MCP rejection and a second GitHub
user's selected repository read/write ceiling. Live callback, secret staging
and Access routing have not been established by the implementation tests.

Protocol references: [GitHub App user authorization](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
and [user installation endpoints](https://docs.github.com/en/rest/apps/installations).
