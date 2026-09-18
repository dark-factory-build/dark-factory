# Repository setup, intake, and community feedback

Implementation boundary, 18 September 2026. This programme extends the existing
Maintainer, local operator API, and host intake controller. GitHub owns the
shared backlog; `factoryd` owns execution. It adds neither a scheduler nor a
general account platform.

## Owners and authentication

`dark-factory/web/packages/{client,ui}` own the console and protocol client.
`dark-factory-site` hosts reviewed, packed versions of those packages and owns
the public website. Backend operations ship before a console invokes them.

The existing Maintainer Worker owns GitHub App keys, installation tokens and
remote operation receipts. Extend it with GitHub App user authorization and
connection-bound delegation, using server-verified user identity, installation
access and repository permission. Installation visibility alone never grants
publication. Native GitHub installation settings remain the repository-access
editor; selected-repository installations remain the baseline.

Use an authorization-code browser flow with a short-lived, single-use state
bound to the initiating host connection. Both CLI and console initiate it through
the same private operator API. The broker retains expiring user authorization
and refresh credentials; the host stores only its connection credential through
the private credential mechanism. Every remote read, write, receipt observation
and reconciliation checks the current connection and delegated repository.
Disconnect and access loss fail closed, including replay. Existing operator
receipts retain their operation IDs and legacy owner; migration cannot make
them visible to customer connections. Cloudflare Access routing must allow the
authorization flow while remote operations still require authenticated authority.
The old operator/service identity is not a customer fallback.

`factoryd` owns project repository bindings, immutable task/source routing,
issue-source configuration and acceptance through SQLite and authenticated
operator operations. Local paths and private repository metadata use private
settings reads, not public snapshots. A connection is an access ceiling; a
project repository is a codebase and checkout; an issue source is a backlog,
filter, acceptance policy and destination. These are distinct boundaries.

The existing host controller remains the sole writer of its reconciliation
journals. It reads source settings and accepted snapshots through the daemon,
then uses the existing task queue and stop/recovery controls. Package its scripts
and generic supervision instructions with the product and manage its existing
launchd job through installation. Browsers never edit controller files.
Operator-owned Git transport authentication remains separate from Maintainer
publication readiness; no worker receives a fetch credential workaround.

## Migration and delivery order

1. Narrow the existing installation check to each operation's actual permission
   set, then add connection authentication and receipt isolation. Preserve the
   existing configured merge/deploy paths; basic reads and reviewed publication
   must not require optional merge/deploy permissions.
2. Migrate each project root to its initial repository binding without changing
   project, task or Change IDs. Bind queued tasks, retained Changes and content
   to their original repository. Defaults affect only new work. Disable blocks
   new selection; removal refuses historical references and never deletes files.
3. Extend the current intake path with sources, preview and snapshot acceptance.
   New sources default to manual acceptance. Legacy settings preserve their
   author/filter behavior and existing journal history, without backfilling an
   old backlog. Accepted identity uses the canonical issue and destination, not
   source configuration ID. Title/body changes require fresh acceptance;
   comments, reactions and activity timestamps never create work. Execution
   consumes the accepted bytes, and cross-repository publication carries a
   qualified reference without automatically closing the shared source.
4. Community reporting and public backlog can proceed independently after UI
   ownership is established. Reports open native GitHub template forms for
   human submission; votes use native reactions. Fixed public-only backlog
   reads work without a daemon or execution subscription.
5. Each implementation PR receives independent adversarial review of its exact
   head, the existing review attestation and affected gates before protected
   merge. Deploy and verify exact revisions separately. Capture desktop/mobile
   browser evidence and distinguish fixture isolation from live second-user
   proof. Mutation tests use disposable homes and designated repositories.

The inspected baseline is runtime `f7db6101` and site `b35e0c9`. Both primary
checkouts contain unrelated work; programme branches use isolated worktrees.
Open runtime PRs #818 (Maintainer identity), #870 (floor heading) and #872
(console copy) must be preserved. Current releases ship three binaries, while
intake and the Maintainer bridge depend on separately installed host assets.
The current broker accepts only its configured Access operator/service identity;
deployed Access/App settings require independent inspection and activation
evidence. No connection, migration or release is claimed complete by this note.
