# dark-factory-relay

A separate Cloudflare Worker that lets a paired browser reach a `factoryd`
installation that has no inbound network path. It is deployed independently of
the GitHub Maintainer control plane in `control-plane/`, carries no GitHub,
webhook, or provider credential, and never interprets Dark Factory messages.

```text
PWA (controller) ──WSS──▶ Worker ──▶ FactoryRelay Durable Object ◀──WSS── factoryd (host, outbound)
```

One Durable Object exists per **factory node** (one `factoryd` home). It holds
one current host socket and many controller sockets, and forwards opaque frames
between them. Projects, state, HumanRequests, terminals, and commands stay
entirely inside `factoryd`; the relay stores none of them, logs none of them,
and queues nothing while a peer is away.

## Identities

| Name | Owner | Shape |
| --- | --- | --- |
| node key | `factoryd` home | Ed25519 key pair; the public key is the factory's relay identity |
| node id | derived | lowercase RFC 4648 base32, no padding, of the first 20 bytes of `SHA-256(node public key)`; exactly 32 characters `[a-z2-7]`; names the Durable Object |
| controller id | `factoryd` | 16 random bytes minted at pairing; the daemon's client identity for one PWA installation |
| device key | PWA | non-exportable ECDSA P-256 key; the public key is its 65-byte uncompressed SEC1 point |
| host generation | `factoryd` | `(generation, sequence)`: `generation` is the daemon's start time in unix seconds, `sequence` counts dials within one boot |

The node id is self-certifying: a Durable Object accepts a host only when the
presented public key hashes to the object's own name, so the relay needs no
directory of factories. An account-backed directory can later map users to
node ids without changing this topology.

## Tokens

Every credential is `base64url(payload) + "." + base64url(signature)` with no
padding, so it is a legal WebSocket subprotocol token. Payloads are UTF-8 JSON.
The signed bytes are a fixed domain prefix followed by the exact base64url
payload text, so verification never re-serialises JSON.

| Token | Signer | Domain prefix | Payload members |
| --- | --- | --- | --- |
| host | node key (Ed25519) | `dark-factory-relay/host\n` | `node`, `key` (base64url 32-byte public key), `generation`, `sequence`, `issued` (unix seconds) |
| ticket | node key (Ed25519) | `dark-factory-relay/ticket\n` | `node`, `controller` (base64url 16 bytes), `purpose` (`"pair"` or `"control"`), `ticket` (base64url 16 bytes), `expires` (unix seconds), and for `control` only `device` (base64url 65-byte SEC1 point) |
| proof | device key (ECDSA P-256, SHA-256, raw 64-byte `r‖s`) | `dark-factory-relay/proof\n` | `ticket` (the ticket id), `issued` (unix seconds), `nonce` (base64url 16 bytes) |

Clock skew tolerance for `issued` is 60 seconds either way. Unknown payload
members are ignored; missing or mistyped required members are rejected.

## Connections

`GET /host/<node id>` — the outbound `factoryd` connection. It must carry
`Sec-WebSocket-Protocol: dark-factory-relay, <host token>` and no `Origin`
header. The object verifies the token, requires `(generation, sequence)` to be
strictly greater than the last accepted pair, then closes any previous host
socket with code 4000 and every controller socket with code 4001. Replaying a
token, presenting an older boot, or dialing without a strictly newer sequence is
refused with HTTP 403 before any upgrade.

`GET /controller/<node id>` — a PWA connection. It must carry `Origin` equal to
the configured PWA origin exactly, and `Sec-WebSocket-Protocol:
dark-factory-relay, <ticket>, <proof>` (`pair` tickets carry no proof). The
object verifies the ticket against the stored node public key, the proof against
the device key named by a `control` ticket, and the ticket expiry. A controller
is refused with HTTP 403 when any check fails, with HTTP 503 when no host is
connected, and with HTTP 429 when the factory already holds 32 controller
sockets or the controller already holds 4. The selected subprotocol is always
`dark-factory-relay`. The relay does not remember spent tickets or revoked
controllers; the daemon that minted them is the durable authority on both.

Every accepted socket uses the Durable Object WebSocket Hibernation API. The
socket attachment holds only `role`, `connection`, and `controller`; sockets are
tagged `host`, `controller`, and `c:<connection>` so routing survives
hibernation without in-memory maps. Text `ping` frames are answered with `pong`
by `setWebSocketAutoResponse` on both roles.

## Envelope

The host socket carries binary messages made of one or more records so many
small frames can share one message:

```text
record  := type(u8) connection(u32 BE) length(u32 BE) payload(length bytes)
message := record+
```

| Type | Direction | Connection | Payload |
| --- | --- | --- | --- |
| `0x01 OPEN` | relay → host | new id | JSON `{"controller":"<base64url>","purpose":"pair"\|"control","origin":"<the exact Origin header the relay validated>"}` |
| `0x02 TEXT` | both | id | one application text frame, verbatim |
| `0x03 BINARY` | both | id | one application binary frame, verbatim |
| `0x04 CLOSE` | both | id | JSON `{"code":<int>,"reason":"<text>"}`; may be empty |
| `0x05 REVOKE` | host → relay | 0 | JSON `{"controller":"<base64url>"}` |

A controller socket carries application frames only: the relay wraps each
inbound frame into one `TEXT` or `BINARY` record for the host and unwraps host
records into frames of the same kind. Connection ids are random nonzero 32-bit
values unique among live controller sockets of one object.

Closing: a controller close produces one `CLOSE` record; a host `CLOSE` record
closes that controller with the given code when it lies in 3000–4999 and with
4005 otherwise; a host socket loss closes every controller with 4001. A
`REVOKE` record closes every socket of that controller id with 4002.

Bounds are exact and fail closed: a controller message larger than 64 KiB, a
host message larger than 4 MiB, a record payload larger than 1 MiB + 64 bytes,
a truncated record, an unknown type, a text frame other than `ping` **on the
host socket** (a controller's text frames are application data and are wrapped
verbatim), or a controller sending more than 120 messages in a burst or 60 per
second sustained ends the offending socket (4003 for limits, 4004 for protocol)
and, for a host, every controller with 4001.

## Transport frames

The relay never interprets any of this. On a controller session the factory
forwards every daemon frame untouched, and additionally sends one text frame
`{"type":"RELAY_TICKET","ticket":"<control ticket>"}` right after
`PAIR_RESULT` or `AUTH_RESULT`. The controller's relay transport consumes that
frame and never hands it to the session, so no session decoder has to tolerate
a member it does not own.

A pairing invitation is a link the factory prints and the controller scans:

```text
https://app.darkfactory.build/remote#df_remote&node=…&daemon=…&challenge=…&ticket=…&expires=…
```

The fragment is plain query pairs (`URLSearchParams`). `relay` and `host` are
optional and omitted at their defaults, `wss://relay.darkfactory.build` and
`127.0.0.1:43123`; `ticket` is a `pair` ticket and `challenge` is the daemon's
own pairing challenge, which is what actually authorizes the pairing.

## Storage and logging

The object persists exactly one record: `host` (`key`, `generation`,
`sequence`). There is no ticket list and no deny list, so storage is O(1) per
factory and cannot grow with traffic. Nothing else is written, and no
application frame, token, or payload is ever logged.

## Deployment

`wrangler.jsonc` names the Worker `dark-factory-relay`, binds `FACTORY_RELAY`
to the `FactoryRelay` class, and sets the single variable `PWA_ORIGIN`. There
are no secrets. `scripts/local-ci.sh` installs dependencies, type-checks,
proves a dry-run deploy, and runs the integration tests against a real
`wrangler dev --local` process.
