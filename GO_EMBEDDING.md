# Embed the Go agents

Import `example.com/edge-delta-lab/publisher`, `receiver`, and (for receiver
transport) `hubclient`. These packages run agents in your process; they do not
invoke the CLI. This remains an experimental updater, not a production SDK.

## Runnable examples

The [publisher](examples/go-embedding/publisher/main.go) and
[receiver](examples/go-embedding/receiver/main.go) are complete programs in a
separate Go module. They import only public packages. Both use a signal-owned
context and handle normal cancellation without treating it as failure.

From the repository root:

```sh
(cd examples/go-embedding && go test -race ./... && go build ./... && go vet ./...)
go run ./cmd/edgelab keygen --out work/keys
(cd examples/go-embedding && go run ./publisher \
  --registry https://registry.example.test --repo app \
  --root /absolute/shared/origin --state /absolute/publisher-state \
  --key /absolute/path/to/work/keys/publisher.key)
```

Run the receiver separately, after provisioning its public signing key, hub
endpoint and TLS identity:

```sh
cd examples/go-embedding
go run ./receiver \
  --hub https://hub.example.test \
  --manifest https://hub.example.test/releases/desired.json \
  --state /absolute/receiver-state --pub /absolute/keys/publisher.pub \
  --hub-ca /absolute/tls/ca.pem \
  --hub-client-cert /absolute/tls/device.pem \
  --hub-client-key /absolute/tls/device.key
```

Replace the example endpoints and absolute paths. The examples build without a
registry, Docker, or hub; running publication needs a populated registry and
running reconciliation needs a separately operated hub serving the shared root.
For a public-CA hub without mTLS, omit all three TLS flags. The example receiver
stages verified artifacts only; it does not enable Docker import.

## Lifecycle and ownership

- Construct with `publisher.New(publisher.Config{...})` or
  `receiver.New(c)` after starting `c` with `receiver.DefaultConfig()`.
  Constructors validate configuration and read key material, without launching
  workers or opening network connections. Config values and publisher repository
  entries are copied; callbacks and the event writer remain caller-owned.
- `Agent.Run(ctx)` blocks. Run each agent in a caller-owned goroutine if needed,
  cancel its context to stop, and wait for its return before releasing resources.
  Cancellation returns `ctx.Err()`; use `errors.Is(err, context.Canceled)`.
  The optional receiver websocket worker is joined before return. Local archive,
  verification and durable file commits can delay shutdown until safe completion.
- Concurrent `Run` calls on the same agent are rejected. After return, the same
  agent can run again. Keep publisher root/state/sequence paths single-writer
  across processes. Keep each receiver state directory exclusive to one device
  and one active agent, including agents constructed separately.
- State is durable: preserve watcher state, sequence files, signed releases,
  receiver journal, verified cache and receipt outbox across restarts. Cancellation
  does not erase them. Agents close their network resources, not your event writer.
- The publisher reads `PasswordEnv` at `Run`; configure it with `Username` for
  registry authentication. Do not put credentials in URLs or logs.

## Errors and logging

Publisher `Event(kind, detail)` reports publication lifecycle and watcher retry
errors. Startup/state errors return from `Run`; scan/publication failures use the
watcher's capped retry backoff. A nil callback disables these logs.

Receiver `OnError(error)` reports failed reconciliations before retry at `Poll`.
`OnSync(Result)` reports completed reconciliation, including release, sequence,
phase and artifact path. `Events io.Writer` receives transfer NDJSON and signed
announcement acceptance/rejection events. Nil hooks disable the corresponding
output. Callbacks are synchronous: keep them short, do not call `Run` from them,
and do not close or mutate shared writers while an agent runs. The receiver
serializes its own event writes; callers must coordinate any other writer users.
Websocket reconnects do not replace polling; hints only trigger reconciliation.

## Trust boundaries

The hub is a **separate service**. The publisher writes a local/mounted hub root;
it does not upload to a remote hub and does not carry hub TLS credentials.
Registry discovery, bearer-token exchange and image export use their own registry
client with system TLS trust. Hub client certificates must never be reused for
registry or token authorities. `watch-registry` rejects nonempty `--hub-*` options
or equivalent YAML keys because that command has no outbound hub connection.

Receiver `HubTLS` applies to explicitly configured hub manifest, chunk, receipt
and websocket endpoints. Treat every configured endpoint as trusted to receive
that identity; do not point a receipt or events URL at an unrelated service.
`hubclient.New` is for hub requests only, not a generic registry client. Its HTTP
redirects are disabled. Hostname verification remains enabled; an optional CA
adds to system roots. A client certificate and key must be provided together.
Keys are PEM regular files with mode `0600` or stricter (no execute/group/other
bits). Test TLS fixtures generate `0600` keys; provision real TLS keys the same
way. Artifact signing keys are separate hex-encoded Ed25519 files: `keygen`
creates private keys at `0600` and public keys at `0644`. Existing Compose setup
uses group-readable `0640` signing keys; those are not TLS client keys.

TLS material forbids HTTP/WS downgrade. Without TLS material, plaintext receiver
traffic still requires explicit `AllowHTTP`; use it only in an isolated lab.
CLI receiver commands `sync`, `watch`, and `conf` accept `--hub-ca`,
`--hub-client-cert`, and `--hub-client-key`; YAML uses `hub_ca`,
`hub_client_cert`, and `hub_client_key`. Recreate the agent/client after identity
rotation. TLS does not replace the pinned signing key, anti-rollback checks,
exact-size checks or encoded/raw/whole-artifact hashes.

`DockerLoad` is opt-in and gives the importer privileged access to Docker. A
`loaded` result is not proof that an application is running or healthy. The Go
integration tests exercise registry publication, mTLS staging and receipt delivery;
they are not Docker end-to-end evidence.
