# Remote discovery

## Capability levels

The first release supports Linux Development Hosts. Discovery tries `/proc`, then `ss`, then `lsof`, and reports the resulting capability explicitly. A listener may remain visible without Process Metadata, but process-dependent policies cannot match it. If scanning is unavailable, remembered Auto-forward policies stay in place and resume when discovery recovers.

## Agentless scanner

For each Forwarding Session, the core streams one fixed scanner script through system SSH using `sh -s`. Host aliases, ports, paths, and Process Metadata never alter the script text; observation stdout remains separate from diagnostic stderr. The scanner uses bounded `SF1` tab-delimited frames with hex-encoded metadata, so Development Host text never becomes shell or framing syntax. It installs no binary or service, modifies no shell profile, requests no elevated privileges, and leaves no product file after the session ends. The scanner observes only the SSH session's process and network namespaces; it does not enter Docker, Podman, or another user's namespaces. Container listeners explicitly published into the observed namespace remain discoverable.

## Cadence and degradation

A healthy session produces an observation about every two seconds. Each cadence first computes a low-cost listener fingerprint from `/proc/net/tcp` and `/proc/net/tcp6`, falling back to `ss` and then `lsof` when those tables are unavailable. Expensive process attribution through `/proc/<pid>/fd` runs when the listener fingerprint changes, on bounded backoff while a visible listener lacks Process Metadata, and on a slower periodic refresh so `exec` or ancestry changes cannot remain stale indefinitely. Stable observations otherwise reuse the last bounded attribution result. This preserves sub-three-second listener detection without paying full process-discovery cost every cadence while the host is idle.

Scans never overlap. If scan cost becomes excessive, cadence slows and the host reports degraded discovery. Scanning stops when the session disconnects and is independent of whether the Dashboard is open.

## Protocol safety

Remote observations are untrusted protocol input. Frames, queued facts, listener counts, paths, argument data, and process-chain depth are bounded and schema-validated; invalid UTF-8 and unsupported versions are rejected. A published host retains at most 256 Listener Observations, 512 Process Metadata records, and 128 KiB of decoded Process Metadata; the parser uses those same core limits. A socket inode is used only inside one observation to associate process records; the parser rejects one nonzero inode attributed to different listener endpoints. Process Metadata is data only and is never interpolated into a shell command. Default logs omit full working directories, argument vectors, and environment data. Long command lines, ancestor chains beyond depth 16, missing ancestry, and retained-evidence truncation downgrade capability to `partial`. During a partial scan, missing evidence does not remove a previously observed Remote Listener, but bounded retention cannot grow into unbounded history. One invalid observation degrades discovery; three consecutive invalid observations—including unsupported-version observation starts—stop parsing and mark discovery failed while stdout remains drained and SOCKS forwards remain usable. A valid complete observation resets the consecutive-error count.

## Address families

Discovery models IPv4 and IPv6 loopback and wildcard bindings: `127.0.0.1`, `::1`, `0.0.0.0`, and `::`. A Local Endpoint attempts to hold both `127.0.0.1` and `::1` at one Allocated Local Port. A real conflict in either supported family rejects that candidate and advances bounded port allocation; systems without IPv6 may degrade to IPv4-only operation.
