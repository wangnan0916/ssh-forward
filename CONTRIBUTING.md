# Contributing

The current surface is the CLI. Keep changes inside that contract unless a document in `docs/` is being updated to match.

Development path (do not skip ahead): **CLI → WebUI → macOS desktop**. The WebUI starts after the CLI is ready; the desktop starts after the WebUI is ready. There is no TUI. See [docs/product/mvp.md](docs/product/mvp.md).

## Development Host language

Use the terms in [CONTEXT.md](CONTEXT.md). In particular: Development Host, Remote Listener, Local Endpoint, Forwarding Policy, Managed Forward.

## Tests

Fast tests do not need Docker:

```bash
cd cli
go test ./...
go test -race ./...
```

Linux/OpenSSH integration tests use only the disposable container harness and never resolve a configured Development Host:

```bash
./scripts/test-integration
```

That harness needs Docker Engine 28 or newer on a local Unix socket.

Behavior tests should drive `core.Manager` (`Snapshot`, `Watch`, `Close`) or an Adapter interface. Do not assert on actor mailboxes or unexported fields.

## Pull requests

- Match the surrounding style and comments.
- Update the implementation-status map in the relevant `docs/product/` file when a documented behavior lands or is deferred.
- Do not add analytics, credential storage, or a second SSH stack.
