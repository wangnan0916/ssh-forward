# SSH Forwarding

This context describes how eligible TCP listeners on a remote development machine become reachable on a user's local machine, preferring the same port.

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
The current health of Remote Listener observation for one Development Host, independent of whether its Forwarding Session can still carry Manual Forwards.
_Avoid_: Connection State, Forwarding Session State

**Discovery Capability**:
The observation evidence currently available for a Development Host, distinguishing Remote Listener visibility from socket and process attribution.
_Avoid_: Discovery State, permission level

**Capability Reason**:
The explanation of why a Discovery Capability dimension is not full, distinguishing scanner-declared partiality from evidence the scanner saw as missing and from evidence core dropped at retention caps. It is translated once, by the host actor, into the Discovery Diagnostic shown on the wire.
_Avoid_: Diagnostic, failure reason, partiality flag

**Listener Observation**:
A point-in-time snapshot of a Remote Listener and any processes observed holding its sockets. Process information may be absent, ambiguous, or change between observations.
_Avoid_: Service record, stable process identity

**Discovery Baseline**:
The first complete set of Listener Observations after connecting to a Development Host. It distinguishes pre-existing listeners from listeners that appear later.
_Avoid_: Saved state, Forwarding Policy

**Listener Lifetime**:
The period during which a Remote Listener retains continuity across successful observations, including a short disappearance grace period. It ends when the endpoint disappears or all previously observed Socket Identities are replaced, even if the same remote port remains occupied.
_Avoid_: Process lifetime, Forwarding Session

**One-time Approval**:
A user's decision to create a Managed Forward for the current Listener Lifetime without saving a Forwarding Policy.
_Avoid_: Manual Forward, Auto-forward policy

**One-time Suppression**:
A user's decision not to ask again during the current Listener Lifetime, without saving an `Ignore` policy.
_Avoid_: Ignore policy, permanent exclusion

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
A saved, prioritized rule whose conditions evaluate a Listener Observation and yield `Auto-forward`, `Ask`, or `Ignore`. Conditions within a policy all must match; no matching policy defaults to `Ask`.
_Avoid_: Forward, Active Forward

**Managed Forward**:
A Forward created from a Listener Observation by a Forwarding Policy or One-time Approval. Its lifetime is reconciled with the corresponding Remote Listener.
_Avoid_: Manual Forward, permanent rule

**Manual Forward**:
A Forward explicitly requested outside policy reconciliation, optionally for a remote loopback port that has not been observed. It remains until the user removes it or its Forwarding Session ends.
_Avoid_: Managed Forward, saved policy, arbitrary remote tunnel

**Local Port Conflict**:
The state in which no permitted Local Endpoint can be allocated because strict same-port allocation was required or the bounded fallback range was exhausted.
_Avoid_: Remote failure, forwarding failure
