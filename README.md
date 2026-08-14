# ssh-forward

A private, greenfield remote-development localhost bridge.

`ssh-forward` discovers eligible TCP listeners on a Linux Development Host, explains their process context, applies explicit forwarding policies, and makes them available on the user's Local Machine. It is editor-independent and uses system OpenSSH rather than implementing SSH or storing credentials.

## Status

The repository currently contains the accepted product and architecture baseline. Production code has not started. The pre-existing shell utility is an unrelated legacy tool and is intentionally not reused, migrated, controlled, or uninstalled by this project.

The private MVP targets one Apple Silicon Mac running macOS Tahoe and one Linux Development Host.

## Design

- [Domain language](./CONTEXT.md)
- [Private MVP](./docs/product/mvp.md)
- [Implementation sequence](./docs/design/implementation-sequence.md)
- [Core Manager Interface](./docs/design/core-interface.md)
- [ADRs](./docs/adr/)
- [Threat model](./docs/security/threat-model.md)

## Product boundary

This is not a general SSH tunnel, credential, key, or host-profile manager. Its center is agentless listener discovery, explainable `Auto-forward` / `Ask` / `Ignore` policy, and lifecycle-aware local forwarding.
