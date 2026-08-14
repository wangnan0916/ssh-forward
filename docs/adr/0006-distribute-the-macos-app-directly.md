# Distribute the macOS app directly

The first macOS desktop release will use Developer ID signing, hardened runtime, and notarized direct distribution rather than the Mac App Store. Direct distribution preserves predictable access to system OpenSSH, SSH configuration, agents, and the bundled headless core without maintaining a sandbox-restricted product variant. A later App Store edition is not ruled out, but it must not weaken or silently change SSH behavior.
