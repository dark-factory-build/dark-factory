# Hosted-origin loopback connectivity spike

This disposable harness answers one narrow question: can a real HTTPS page
open `ws://127.0.0.1:<port>/browser/v1`? It is not production browser
transport or product UI. The server binds only `127.0.0.1`, accepts one exact
Host (including port) and one exact Origin, and echoes bounded binary frames.
There is no pairing or credential challenge.

## Run the local probe

From this directory, choose an unused disposable port and an origin that the
preview page will actually have:

```sh
go run . -port 43123 -origin https://app.darkfactory.build
```

The first line is readiness JSON, for example:

```json
{"url":"ws://127.0.0.1:43123/browser/v1","expected_host":"127.0.0.1:43123","expected_origin":"https://app.darkfactory.build","path":"/browser/v1","max_frame_bytes":65536}
```

After readiness, each accepted or refused upgrade and each bounded frame or
close produces one JSONL evidence event. Events contain only classification,
Host/Origin/path/status, byte counts, close codes, and a neutral connection
index; they never dump request headers or payloads. The writer is serialized
and readiness is emitted before the listener accepts requests.

Serve or preview `probe.html` from that HTTPS origin. If using another port,
replace only `LOOPBACK_WS_URL` in the fixture with the readiness `url`; never
put a token, challenge, or credential in the URL. Press **connect**, **send
binary echo**, **close**, and **reconnect**. Record the page's state log and the
server readiness line as evidence.

## Evidence matrix

At minimum, record headed current Chrome results for: exact Host and Origin,
missing/`null`/wrong Origin, wrong Host, masked binary input and binary echo,
oversized and malformed frames, close/reconnect, and server shutdown. Repeat
with the hosted preview origin and note mixed-content or Local Network Access
prompts and their reset/grant/deny behavior. A page reload is a reconnect only
while the same disposable server remains running. Safari/WebKit is observed
separately and is not claimed by this fixture.

The harness intentionally does not use a browser automation dependency. Keep
the server process, port, and any preview state temporary; do not point it at
the Dark Factory live home, daemon, socket, provider, or credentials.

If a thin HTTPS host applies a strict Content Security Policy, permit this
fixture's inline script with its exact SHA-256 CSP hash (recompute it whenever
`probe.html` changes); do not weaken the policy with `unsafe-inline` or add a
frontend framework. The fixture loads no third-party resource.

## Focused checks

```sh
gofmt -w *.go
go test ./...
go vet ./...
git diff --check
```

The tests use real loopback sockets and an in-process raw WebSocket client.
