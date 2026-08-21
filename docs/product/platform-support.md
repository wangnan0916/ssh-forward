# Platform support

## First public CLI

The public CLI targets:

- Local Machine: macOS or Linux, with a system OpenSSH client
- Architecture: exercised on Apple Silicon macOS (unit CI) and Linux (integration CI)
- Development Host: Linux over system OpenSSH

Windows as a Local Machine is not offered. The future desktop app is not in this repository yet and may ship on current macOS only.

The original private self-use spike targeted one Apple Silicon Mac running macOS Tahoe 26.6.1. That machine remains the performance-budget reference (docs/product/performance-budget.md); it is not a compatibility claim for the public CLI.

## Portability discipline

The Go core keeps platform behavior behind Adapters and build-tagged files so machine-specific assumptions do not enter policy, observation, reconciliation, or Manager domain code. Windows local Adapters are deferred rather than simulated without hardware. Remote Linux behavior is covered by default through disposable containerized Ubuntu SSH hosts shared by local and CI integration tests. Automated tests never connect to a configured Development Host. Any real-host smoke test requires a separate, explicit user authorization.
