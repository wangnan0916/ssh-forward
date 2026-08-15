# ssh-forward

A private, greenfield remote-development localhost bridge.

`ssh-forward` discovers eligible TCP listeners on a Linux Development Host, explains their process context, applies explicit forwarding policies, and makes them available on the user's Local Machine. It is editor-independent and uses system OpenSSH rather than implementing SSH or storing credentials.

## Status

The repository contains the accepted product and architecture baseline. The transport spike and the first four vertical slices are complete. The Go Manager runs a fixed agentless scanner through one system-OpenSSH Forwarding Session, publishes complete Discovery and Manual Forward Snapshots, and exposes bounded capability-negotiated JSON-RPC Watch streams. Go-owned dual-stack Local Endpoints, SOCKS cancellation, TCP half-close, reconnect retention, scanner fallback/evidence handling, and disposable IPv4/IPv6 integration are covered. Listener Lifetime and Policy reconciliation are next. The pre-existing shell utility is unrelated and is intentionally not reused, migrated, controlled, or uninstalled.

The private MVP targets one Apple Silicon Mac running macOS Tahoe and one Linux Development Host.

## Design

- [Domain language](./CONTEXT.md)
- [Private MVP](./docs/product/mvp.md)
- [Implementation sequence](./docs/design/implementation-sequence.md)
- [Transport spike verdict](./docs/design/transport-spike-verdict.md)
- [Core Manager Interface](./docs/design/core-interface.md)
- [ADRs](./docs/adr/)
- [Threat model](./docs/security/threat-model.md)

## Development

Fast tests do not require Docker:

```bash
cd cli
go test ./...
go test -race ./...
```

Real Linux/OpenSSH integration tests use only the disposable container harness and never resolve a configured Development Host:

```bash
./scripts/test-integration
```

The integration harness requires Docker Engine 28 or newer through a local Unix socket. Local development and Linux CI run the same command and pinned Ubuntu test image.

## Product boundary

This is not a general SSH tunnel, credential, key, or host-profile manager. Its center is agentless listener discovery, explainable `Auto-forward` / `Ask` / `Ignore` policy, and lifecycle-aware local forwarding.
