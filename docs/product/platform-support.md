# Platform support

## Private MVP baseline

The private self-use MVP targets the current development machine only:

- Local Machine: macOS Tahoe 26.6.1
- Architecture: Apple Silicon (`arm64`)
- Memory: 32 GiB
- Development Host: Linux over system OpenSSH

The prototype and first MVP make no compatibility claim for older macOS releases, Intel Macs, Linux as a Local Machine, or Windows. Desktop deployment may therefore target macOS 26 initially. Broader minimum-version support is a later product decision based on actual demand.

## Portability discipline

The Go core must still keep platform behavior behind private Adapters and build-tagged files so Tahoe-specific assumptions do not enter policy, observation, reconciliation, or Manager domain code. Linux and Windows local Adapters are deferred rather than simulated without hardware. Remote Linux behavior is covered through opt-in integration tests against the configured Development Host.
