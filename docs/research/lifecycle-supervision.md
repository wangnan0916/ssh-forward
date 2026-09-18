# Lifecycle supervision evaluation

## Decision

Keep the current context-based worker lifecycle. Do not add Suture to production
for this refactor. Configuration scopes and ANSI truncation are independent
changes and do not require a supervisor framework.

## Prototype

Evaluated `github.com/thejerf/suture/v4` v4.0.6 in an isolated copy of the Go
module. One supervisor per host owned discovery and forwarding tasks. The
prototype replaced the two retry loops and WaitGroup with supervised tasks,
`ServeBackground`, and context cancellation. The reconciliation planner, status
synchronization, connection generation checks, and backend cleanup remained.

To approximate existing retries, the supervisor used FailureThreshold 0.1 and
FailureBackoff equal to the manager retry delay; its shutdown timeout was three
seconds. These settings are prototype choices, not a replacement contract.

The adapted `core/manager.go` was 373 lines versus the original 379: only six
lines removed before completing integration. The existing core and app tests
passed with the race detector, including multi-host isolation.

## Boundary failure

A new regression, `TestRemoveFailedForwardDoesNotWaitForRetry`, sets a 30-second
retry delay, waits for a failed forward, then removes its rule. Removal must
clear the worker without waiting for retry. The existing implementation passes;
the prototype failed the one-second assertion.

The prototype only cancelled the worker context. A service waiting in Suture's
restart queue has no running Serve call to observe that cancellation and notify
the planner. A complete integration would need explicit service tokens,
removal, and completion handling without declaring an active forward stopped
before backend cleanup finishes. Suture provides removal APIs; this is a gap
in the minimal integration, not evidence that the library cannot support it.

Other differences require a deliberate policy: failure backoff is shared by
siblings within a supervisor, and supervisor shutdown can time out while a
service is still running. Our current cleanup waits for workers before closing
the backend, even if an individual caller stops waiting.

The small initial line reduction does not justify that additional integration
for this project. Revisit if a larger set of long-lived services needs a shared
supervision policy. Keep the regression in the production test suite.

References: [Suture](https://github.com/thejerf/suture) and
[Supervisor API](https://pkg.go.dev/github.com/thejerf/suture/v4).
