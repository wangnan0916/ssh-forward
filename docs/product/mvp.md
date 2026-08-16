# Private MVP

The first useful release replaces the developer's daily VS Code Ports workflow for one selected Linux Development Host from a macOS Tahoe Local Machine.

## Included

- One SSH alias and automatic monitoring at login
- Agentless Remote Listener discovery with process and working-directory context
- Discovery Baseline, Ask, Forward once, Ignore once, and persistent Auto-forward/Ignore policies
- Preferred local port with bounded fallback
- Directory, direct-process, and ancestor-process matchers
- Managed and Manual Forward lifecycle
- Menu-bar quick panel and full Dashboard with Needs Attention, Active, Available, and Ignored states
- Explicit HTTP/HTTPS browser actions
- Reconnection, local diagnostics, configuration editing, and policy explanation

## Excluded

- Multi-host aggregate desktop UI
- Reverse, dynamic, or arbitrary-destination tunnel management
- Credential/key/profile management
- Accounts, cloud sync, analytics, automatic updater, and public distribution
- Linux or Windows Local Machine releases
- Backward compatibility or migration from the legacy utility

## Implementation status

The Included list is the first release's scope, not the current state. This map records which items exist today and which land with their slices (design/implementation-sequence.md):

Enforced today:

- Agentless Remote Listener discovery with process and working-directory context (slices 1–4).
- Preferred local port with bounded fallback (ADR-0008, core/forward_ownership.go).
- Manual Forward lifecycle (core/, jsonrpc/, proxy/).
- Reconnection with classified failures and Listener Lifetime verdicts on the wire.
- Forwarding Policy evaluation with directory, direct-process, and ancestor-process matchers; policies.jsonc (versioned, ADR-0005); Ask state on the wire; One-time Approval and Suppression; Managed Forward reconciliation with observation-and-clock hysteresis (slice 5).
- The domain-oriented CLI with wire-shaped --json output (slice 6, cli/cmd/ssh-forward/).

Lands with later slices:

- One SSH alias and automatic monitoring at login: desktop phase.
- Menu-bar quick panel and full Dashboard with Needs Attention, Active, Available, and Ignored states: slices 6–7 (desktop phase).
- Explicit HTTP/HTTPS browser actions: desktop surfaces (policy surfaces).
