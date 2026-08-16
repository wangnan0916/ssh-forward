# ssh-forward

A private, greenfield remote-development localhost bridge.

`ssh-forward` discovers eligible TCP listeners on a Linux Development Host, explains their process context, applies explicit forwarding policies, and makes them available on the user's Local Machine. It is editor-independent and uses system OpenSSH rather than implementing SSH or storing credentials.

## Status

The repository contains the accepted product and architecture baseline. The transport spike and the first four vertical slices are complete. The Go Manager runs a fixed agentless scanner through one system-OpenSSH Forwarding Session, publishes complete Discovery and Manual Forward Snapshots, and exposes bounded capability-negotiated JSON-RPC Watch streams. Go-owned dual-stack Local Endpoints, SOCKS cancellation, TCP half-close, reconnect retention, scanner fallback/evidence handling, per-listener Listener Lifetime verdicts on the wire, and disposable IPv4/IPv6 integration are covered. Policy reconciliation (Forwarding Policies, One-time Approval and Suppression, Ask state) is next. The pre-existing shell utility is unrelated and is intentionally not reused, migrated, controlled, or uninstalled.

The private MVP targets one Apple Silicon Mac running macOS Tahoe and one Linux Development Host.

## Design

- [Domain language](./CONTEXT.md)
- [Implementation sequence](./docs/design/implementation-sequence.md)
- [Core Manager Interface](./docs/design/core-interface.md)
- [JSON-RPC protocol](./docs/design/ipc-protocol.md)
- [Testing strategy](./docs/design/testing-strategy.md)
- [Transport spike verdict](./docs/design/transport-spike-verdict.md)
- [ADRs](./docs/adr/)

### Product

- [Private MVP](./docs/product/mvp.md)
- [Connection lifecycle](./docs/product/connection-lifecycle.md)
- [Remote discovery](./docs/product/remote-discovery.md)
- [Discovery and policy behavior](./docs/product/discovery-and-policy.md)
- [Diagnostics and recovery](./docs/product/diagnostics-and-recovery.md)
- [Performance budget](./docs/product/performance-budget.md)
- [CLI and state](./docs/product/cli-and-state.md)
- [Desktop experience](./docs/product/desktop-experience.md)
- [Platform support](./docs/product/platform-support.md)
- [Repository and releases](./docs/product/repository-and-releases.md)

### Research

- [Library options](./docs/research/library-options.md)
- [IPC library options](./docs/research/ipc-library-options.md)
- [Codinn Tunnel competitive analysis](./docs/research/codinn-tunnel-competitive-analysis.md)
- [VS Code port forwarding](./docs/research/vscode-port-forwarding.md)

### Security

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
