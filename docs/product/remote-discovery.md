# Remote discovery

## Capability levels

The first release supports Linux Development Hosts. Discovery prefers `/proc` plus `ss`, falls back to available `/proc`/`lsof` information, and reports capability explicitly. A listener may remain visible without Process Metadata, but process-dependent policies cannot match it. If scanning is unavailable, Manual Forward remains available.

## Agentless scanner

For each Forwarding Session, the core streams one fixed, versioned scanner script through system SSH using `sh -s`. Host aliases, ports, paths, and Process Metadata never alter the script text; bounded structured inputs use stdin or constrained arguments, observation stdout remains separate from diagnostic stderr, and checksum/version is exposed for diagnostics. The scanner installs no binary or service, modifies no shell profile, requests no elevated privileges, and leaves no product file after the session ends. The scanner observes only the SSH session's process and network namespaces; it does not enter Docker, Podman, or another user's namespaces. Container listeners explicitly published into the observed namespace remain discoverable.

## Cadence and degradation

A healthy session produces an observation about every two seconds. Scans never overlap. If scan cost becomes excessive, cadence slows and the host reports degraded discovery. Scanning stops when the session disconnects and is independent of whether the Dashboard is open.

## Protocol safety

Remote observations are untrusted protocol input. Frames, listener counts, paths, argument data, and process-chain depth are bounded and schema-validated; invalid UTF-8 and unsupported versions are rejected. Process Metadata is data only and is never interpolated into a shell command. Default logs omit full working directories, argument vectors, and environment data. Repeated invalid observations stop discovery and surface a diagnostic state.

## Address families

Discovery models IPv4 and IPv6 loopback and wildcard bindings: `127.0.0.1`, `::1`, `0.0.0.0`, and `::`. A Local Endpoint attempts to hold both `127.0.0.1` and `::1` at one Allocated Local Port. A real conflict in either supported family rejects that candidate and advances bounded port allocation; systems without IPv6 may degrade to IPv4-only operation.
