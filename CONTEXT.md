# SSH Forwarding

This context describes how eligible TCP listeners on a remote development machine become reachable on a user's local machine, preferring the same port.

Development path: **CLI** (current) → **WebUI** (after the CLI is ready) → **macOS desktop** (after the WebUI is ready). Do not start a later surface early. Details: [docs/product/mvp.md](docs/product/mvp.md). There is no TUI (ADR-0021).

## Language

**Development Host**:
The remote Linux machine on which development workloads run.
_Avoid_: Server, remote, target

**Local Machine**:
The user's machine on which forwarded development services are accessed.
_Avoid_: Client, Mac

**Remote Listener**:
An eligible TCP listening endpoint identified by its Development Host, address family, bind scope, and remote port. It may bind loopback or a wildcard address and is identified independently of whichever processes currently hold its sockets.
_Avoid_: Service, remote service, process

**Socket Identity**:
An opaque equality token for one observed listening socket on a Development Host. It remains comparable across Forwarding Sessions only while the Development Host boot and observed network namespace remain the same.
_Avoid_: Inode, socket ID, process identity

**Wildcard Listener**:
A Remote Listener bound to all network interfaces rather than only loopback. It is treated as already exposed on the Development Host and requires an explicit policy before automatic forwarding.
_Avoid_: Public service, loopback listener

**Preferred Local Port**:
The Remote Listener's port, attempted first when allocating its Local Endpoint.
_Avoid_: Guaranteed port, Allocated Local Port

**Allocated Local Port**:
The port actually held by a Local Endpoint after applying the configured conflict policy.
_Avoid_: Remote port, Preferred Local Port

**Local Endpoint**:
The logical loopback endpoint on the Local Machine at the Allocated Local Port for a Remote Listener. It may own both IPv4 and IPv6 listening sockets at that port.
_Avoid_: Local service, remote endpoint

**Forward**:
A connection path from a Local Endpoint to a Remote Listener on one Development Host. It records both the remote port and Allocated Local Port.
_Avoid_: Generic tunnel, arbitrary user-defined mapping

**Active Forward**:
A Forward that currently exists in the live Forwarding Session. It is observed transport state, not remembered user intent.
_Avoid_: Preference, policy, saved rule

**Forwarding Session**:
The live, dedicated SSH connection carrying Active Forwards for one Development Host.
_Avoid_: Terminal session, editor session

**Discovery State**:
The current health of Remote Listener observation for one Development Host, independent of whether its Forwarding Session can still carry traffic.
_Avoid_: Connection State, Forwarding Session State

**Discovery Capability**:
The observation evidence currently available for a Development Host, distinguishing Remote Listener visibility from socket and process attribution.
_Avoid_: Discovery State, permission level

**Capability Reason**:
The explanation of why a Discovery Capability dimension is not full, distinguishing scanner-declared partiality from evidence the scanner saw as missing and from evidence core dropped at retention caps. Core has one Diagnostic table that turns this, observation gaps, and DiscoveryChange reasons into the Discovery Diagnostic shown on the wire.
_Avoid_: Diagnostic, failure reason, partiality flag

**Connection Diagnostic**:
The user-visible explanation of a terminal Forwarding Session failure (invalid alias, authentication, or host-key), independent of Discovery Diagnostic. It is empty while the session is connecting, connected, or retrying.
_Avoid_: SSH stderr, SessionError text, Discovery Diagnostic

**Policy Diagnostic**:
The explanation that the saved Forwarding Policy file is unreadable while last-valid policies remain in effect.
_Avoid_: parse error, file I/O error, Policy Evidence

**Listener Observation**:
A point-in-time snapshot of a Remote Listener and any processes observed holding its sockets. Process information may be absent, ambiguous, or change between observations.
_Avoid_: Service record, stable process identity

**Discovery Baseline**:
The first complete set of Listener Observations after connecting to a Development Host. It distinguishes pre-existing listeners from listeners that appear later.
_Avoid_: Saved state, Forwarding Policy

**Listener Lifetime**:
The period during which a Remote Listener remains the same endpoint across successful observations, identified by Development Host, address family, bind scope, and remote port. Socket Identity is observation evidence, not that identity.
_Avoid_: Process lifetime, Forwarding Session, Socket Identity as listener identity

**Listener Process**:
Any process observed holding a socket associated with a Remote Listener. A listener may have zero, one, or multiple Listener Processes.
_Avoid_: Service, unique owner

**Process Chain**:
A Listener Process together with its observed ancestor processes. It distinguishes the process accepting connections from launchers such as package managers.
_Avoid_: Command string, service identity

**Process Metadata**:
Optional facts about a process in a Process Chain, such as its working directory, executable, or arguments. These facts support policy matching but do not define listener identity.
_Avoid_: Listener identity, service identity

**Policy Evidence**:
The Listener Observation facts used to explain why a Forwarding Policy matched, failed to match, or could not be evaluated reliably.
_Avoid_: Policy, Process Metadata

**Forwarding Policy**:
A saved, prioritized rule whose conditions evaluate a Listener Observation and yield `Auto-forward` or `Ignore`. Conditions within a policy all must match; no matching policy leaves the listener unforwarded.
_Avoid_: Forward, Active Forward

**Remembered Auto-forward**:
A simple Auto-forward Forwarding Policy for one remote port or one Development Host working-directory tree. It is persistent intent, not an Active Forward.
_Avoid_: Manual Forward, one-off tunnel, remembered rule

**Managed Forward**:
A Forward created from a Listener Observation by a Forwarding Policy. Its lifetime is reconciled with the corresponding Remote Listener.
_Avoid_: remembered rule, permanent tunnel

**Local Port Conflict**:
The state in which no permitted Local Endpoint can be allocated because strict same-port allocation was required or the bounded fallback range was exhausted.
_Avoid_: Remote failure, forwarding failure
