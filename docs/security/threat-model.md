# Threat model

## Trust assumptions

The product trusts the current OS user, their SSH configuration, and the system OpenSSH executable selected by the platform Adapter. It does not trust remote Listener Observations, other local users, malformed configuration, or unauthenticated network input. Processes running as the same OS user may access local Manager IPC; the first release does not add a second application token inside that user boundary.

User-configured OpenSSH behavior such as `ProxyCommand` intentionally executes with the user's privileges. The product delegates that trust decision to system OpenSSH rather than partially parsing or sandboxing SSH configuration.

## Local storage and IPC

Configuration and runtime directories must be private to the current user. Configuration, logs, Unix sockets, lock files, and diagnostics reject unsafe ownership, permissions, symlinks, and parent directories. Windows named pipes use a current-user-only ACL. Startup fails closed when these guarantees cannot be established.

Manager IPC, the private SSH SOCKS endpoint, and every Local Endpoint bind only to local OS primitives or loopback; none listens on a non-loopback network interface.

## Process execution

System SSH is launched by a platform-determined absolute path with an argument vector, never through a local shell. SSH aliases are bounded and cannot begin with `-`. The child environment retains only values needed for user configuration, agent access, locale, and askpass behavior. Authentication prompts pass through the ephemeral signed askpass helper and are never persisted or logged.

## Remote scanner

The remote command is one fixed, versioned product scanner invoked through `sh -s`. Host aliases, ports, paths, and Process Metadata never modify the script text. Structured inputs use bounded stdin or constrained arguments. Scanner checksum/version is diagnostic metadata. Observation stdout is framed separately from diagnostic stderr.

All observation fields are bounded and validated before becoming domain values. Process Metadata is display and Policy Evidence only; it is never executed, interpolated into a command, treated as trusted markup, or emitted unredacted in normal logs.

## Out of scope

The product does not defend against compromise of the current OS user, malicious user-owned SSH configuration, a compromised system OpenSSH binary, or a compromised Development Host returning traffic from a service the user explicitly forwards. It minimizes the consequences by binding locally, storing no SSH credential, and keeping remote discovery unprivileged and ephemeral.
