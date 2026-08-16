# Discovery and policy behavior

## Connection baseline

After a Development Host connects, the first complete observation is the Discovery Baseline. Existing policies are applied immediately. Remote Listeners without a matching policy remain visible but do not produce prompts; only listeners first observed after the baseline enter the default `Ask` flow.

## Ask actions

- **Forward once** — create a Managed Forward through One-time Approval for the current Listener Lifetime.
- **Always forward this port** — save a host-scoped `Auto-forward` policy for the remote port.
- **Create policy…** — open an editor prefilled with observed process and working-directory evidence; broad rules require explicit confirmation.
- **Ignore once** — create One-time Suppression for the current Listener Lifetime.
- **Always ignore** — save a persistent `Ignore` policy.

## Initial policy matchers

A policy may match a remote port or range, loopback/wildcard binding, Listener Process executable basename or full path, ancestor-process executable basename or full path, and a Listener Process working directory under a directory tree. Linux path matching is case-sensitive and path-component aware. Arbitrary full-command-line regular expressions are deferred.

Conditions within one policy are combined with `AND`; policies are evaluated by explicit priority and first match. Missing evidence does not match. No match yields `Ask`.

If a Remote Listener has multiple attributable Listener Processes, automatic action requires every attributable process to produce a consistent result. Otherwise the result is `Ask`.

## Local port allocation

A Forward first attempts to bind its Preferred Local Port. On address-in-use failure it atomically attempts each port from `remotePort + 1` through `remotePort + 100`, without a separate check-then-bind step. A policy with `requireSamePort: true` disables fallback. The Allocated Local Port remains stable for the current Listener Lifetime and is shown explicitly whenever it differs from the remote port. Exhausting the permitted range produces Local Port Conflict; the product never terminates the process occupying a candidate port.

## Listener continuity

Listener Lifetime continuity is based on observed sockets rather than port number or PID alone. Hot reload that inherits an existing socket keeps the Lifetime. If every previously observed socket disappears and replacement sockets occupy the same port, a new Lifetime begins; One-time Approval and One-time Suppression do not carry into it. Absent listeners pass through a disappearance grace before the Lifetime ends: three observation cycles of tolerance plus the ending scan, i.e. four consecutive absences, about eight seconds at the scanner's two-second cadence.

## Continuous policy reconciliation

Persistent Forwarding Policies are reevaluated on every valid observation. A determinate policy mismatch or change to `Ignore` removes its Managed Forward after two consecutive observations and five seconds. A saved change is previewed before commit, then reconciles immediately. One-time Approval remains valid for its Listener Lifetime despite Process Metadata changes, and Manual Forward is never reconciled by Policy.

Missing Process Metadata is distinct from scanner failure. A new listener lacking required Policy Evidence enters `Ask`. An existing policy-managed Forward is retained briefly, then removed into Needs Attention after two successful observations and five seconds without the required evidence. SSH or scanner failure never counts toward that threshold.

## Service protocol and action

A Forwarding Policy separates its forwarding decision from post-forward behavior. `protocol` is `tcp`, `http`, or `https`; `onForward` is `none`, `notify`, or `openBrowser`. Defaults are `tcp` and `notify`. Browser opening is valid only for an explicitly declared HTTP/HTTPS service and uses the actual Allocated Local Port.

## Disappearance and cleanup

A Managed Forward is removed only after two consecutive successful scans omit its Remote Listener and at least five seconds have elapsed. Connection loss and scanner failure do not count as disappearance. Reconnection obtains a complete observation before cleanup resumes.
