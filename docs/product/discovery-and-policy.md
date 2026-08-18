# Discovery and policy behavior

## Implementation status

This document describes the Discovery and Policy surface in two registers. Enforced today: the Local port allocation, Listener continuity, Connection baseline, Initial policy matchers, Continuous policy reconciliation, and Disappearance and cleanup sections below (slice 5: core/reconcile.go, core/policy.go, policies.jsonc). Planned: the Service protocol and action section lands with the desktop policy surfaces (explicit browser actions).

## Connection baseline

After a Development Host connects, the first complete observation is the Discovery Baseline.

Slice 5 adds policy application on top of the Baseline: existing policies are applied immediately; Remote Listeners without a matching policy remain visible and are not forwarded. `status` lists those ports as a one-line heads-up (`ssh-forward add PORT`).

## Initial policy matchers

A policy may match a remote port or range, loopback/wildcard binding, Listener Process executable basename or full path, ancestor-process executable basename or full path, and a Listener Process working directory under a directory tree. Linux path matching is case-sensitive and path-component aware. Arbitrary full-command-line regular expressions are deferred.

Conditions within one policy are combined with `AND`; policies are evaluated by explicit priority and first match. Missing evidence does not match. No match leaves the listener unforwarded.

If a Remote Listener has multiple attributable Listener Processes, automatic action requires every attributable process to match the same policy. Otherwise the policy does not match.

## Local port allocation

A Forward first attempts to bind its Preferred Local Port. On address-in-use failure it atomically attempts each port from `remotePort + 1` through `remotePort + 100`, without a separate check-then-bind step. The Allocated Local Port remains stable while the Managed Forward exists and is shown explicitly whenever it differs from the remote port. Exhausting the permitted range produces Local Port Conflict; the product never terminates the process occupying a candidate port. Implemented: `proxy/allocator.go` (ADR-0008); `OpenEndpoint` binds one port. `ForwardSpec.RequireSamePort` is the exact-port seam; no Forwarding Policy field sets it yet.

## Listener continuity

A Remote Listener is identified by Development Host, address family, bind scope, and remote port. Socket Identities travel with each Listener Observation as evidence, not as that identity. Replacement on the same port is a new observation of that Remote Listener; Auto-forward policies re-evaluate it like any other generation.

## Continuous policy reconciliation

Slice 5 adds Policy reconciliation. Persistent Forwarding Policies are reevaluated on every valid observation. On that observation path, a determinate policy mismatch removes its Managed Forward after two consecutive observations. A saved policy edit — including a change to `Ignore` — applies against the current observations immediately and resets hysteresis; it does not wait for the next scan. Desktop preview-before-commit lands with the configuration surface.

Missing Process Metadata is distinct from scanner failure. A new listener lacking required Policy Evidence is not forwarded. An existing policy-managed Forward is retained briefly, then removed after two successful observations without the required evidence. SSH or scanner failure never counts toward that threshold.

## Service protocol and action (desktop surfaces)

A Forwarding Policy separates its forwarding decision from post-forward behavior. `protocol` is `tcp`, `http`, or `https`; `onForward` is `none`, `notify`, or `openBrowser`. Defaults are `tcp` and `notify`. Browser opening is valid only for an explicitly declared HTTP/HTTPS service and uses the actual Allocated Local Port.

## Disappearance and cleanup

A Managed Forward is removed only after two consecutive successful scans omit its Remote Listener. Connection loss and scanner failure do not count as disappearance. Reconnection obtains a complete observation before cleanup resumes.
