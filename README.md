# ssh-forward

A private, greenfield remote-development localhost bridge.

`ssh-forward` discovers eligible TCP listeners on a Linux Development Host, explains their process context, applies explicit forwarding policies, and makes them available on the user's Local Machine. It is editor-independent and uses system OpenSSH rather than implementing SSH or storing credentials.

## Status

The repository contains the accepted product and architecture baseline. The isolated transport risk spike is complete and accepted. Production now has its first forwarding tracer: the `core.Manager` and versioned JSON-RPC Adapter can add, inspect, and remove one-host Manual Forwards through a system-OpenSSH dynamic session. Go-owned dual-stack Local Endpoints, SOCKS cancellation, TCP half-close, bounded drain, reconnect retention, and disposable IPv4/IPv6 integration are covered. The pre-existing shell utility is an unrelated legacy tool and is intentionally not reused, migrated, controlled, or uninstalled by this project.

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
