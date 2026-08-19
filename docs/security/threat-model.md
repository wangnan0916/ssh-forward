# Threat model

## Trust assumptions

The product trusts the current OS user, their SSH configuration, and the system OpenSSH executable selected by the platform Adapter. It does not trust remote Listener Observations, other local users, malformed configuration, or unauthenticated network input. Processes running as the same OS user may access local Manager IPC; the first release does not add a second application token inside that user boundary.

User-configured OpenSSH behavior such as `ProxyCommand` intentionally executes with the user's privileges. The product delegates that trust decision to system OpenSSH rather than partially parsing or sandboxing SSH configuration.

## Local storage and IPC

Configuration and runtime directories must be private to the current user. Configuration, logs, Unix sockets, lock files, and diagnostics reject unsafe ownership, permissions, symlinks, and parent directories. A Windows build, if one exists, uses current-user-only ACLs on named pipes. Startup fails closed when these guarantees cannot be established.

Manager IPC, the private SSH SOCKS endpoint, and every Local Endpoint bind only to local OS primitives or loopback; none listens on a non-loopback network interface.

## Process execution

System SSH is launched by a platform-determined absolute path with an argument vector, never through a local shell. SSH aliases are bounded and cannot begin with `-`. The child environment retains only values needed for user configuration, agent access, locale, and askpass behavior. Authentication prompts will pass through the ephemeral signed askpass helper (desktop phase; design in research/library-options.md) and are never persisted or logged.

## Remote scanner

The remote command is one fixed, versioned product scanner invoked through `sh -s`. Host aliases, ports, paths, and Process Metadata never modify the script text. Structured inputs use bounded stdin or constrained arguments. Scanner checksum/version is diagnostic metadata. Observation stdout uses bounded versioned frames with hex-encoded metadata and remains separate from diagnostic stderr.

Each frame, complete observation, queued fact set, collection count, string, argument vector, Process Chain, and materialized per-host evidence projection is independently bounded and validated before becoming a domain value. Socket-to-endpoint relationships are validated before process evidence is expanded. Repeated invalid observations stop parsing but keep stdout drained so discovery failure cannot fill the SSH channel or terminate SOCKS forwards. Process Metadata is display and Policy Evidence only; it is never executed, interpolated into a command, treated as trusted markup, or emitted unredacted in normal logs.

## Out of scope

The product does not defend against compromise of the current OS user, malicious user-owned SSH configuration, a compromised system OpenSSH binary, or a compromised Development Host returning traffic from a service the user explicitly forwards. It minimizes the consequences by binding locally, storing no SSH credential, and keeping remote discovery unprivileged and ephemeral.

## Implementation status

The sections above state the full-product security posture. This map records which guarantees are enforced today and which land with later slices; it is the checklist that flips as surfaces land.

Enforced today:

- Every Local Endpoint binds only to loopback, and the SOCKS reservation binds 127.0.0.1 (proxy/endpoint.go listenOnLoopback; openssh/adapter.go reserveSOCKSAddress).
- System SSH launches by absolute path with an argument vector, never through a shell (openssh/adapter.go New).
- Host aliases are bounded (MaxHostAliasLength, core/domain.go) and validated as arguments, not script text.
- The remote command is the fixed versioned scanner embedded at openssh/scanner_script.go, invoked through `sh -s`; the script text is never interpolated.
- Bounded versioned frames with hex-encoded metadata keep observation stdout separate from diagnostic stderr (openssh/scanner.go, session.go); repeated invalid observations stop parsing but keep stdout drained.
- Socket-to-endpoint relationships are validated before process evidence expands (core/forward_ownership.go).
- Process Metadata is evidence-only; no credential is stored anywhere.
- The product config directory is created `0700`, and the manager socket is `0600`. A second `manager serve` is refused while a live manager answers; a stale socket file is replaced only after a connection probe proves no live manager owns it (`jsonrpc` Listen). Manager IPC trusts the current OS user (ADR-0017); there is no additional application token.

Lands with later slices:

- Loopback WebUI (`ssh-forward ui start`): bind `127.0.0.1` only, ephemeral port, unguessable URL token; the browser never speaks Manager IPC directly (ADR-0021).
- Askpass prompting: desktop phase (research/library-options.md:136).
- Broader ownership/symlink fail-closed checks on the config directory, logs, and lock files at startup.
- Log redaction ("never emitted unredacted in normal logs"): with the first log sink (research/library-options.md slog design).
