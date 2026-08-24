# Working-directory forwarding library assessment

Date: 2026-08-24

## Conclusion

No mature Go library removes the core domain operation:

```text
remembered ports ∪ listeners whose working directory matches a rule
→ diff against running per-port workers
→ start and stop workers independently
```

The current design should stay small and explicit. The useful simplifications are in the Go standard library:

1. Keep `doublestar` for glob validation and matching.
2. Use `sync.WaitGroup.Go` for goroutine accounting.
3. Consider `testing/synctest` if concurrency tests grow; the current local
   helpers are smaller for this patch.
4. Keep the existing SSH/Docker integration harness. Add a polling library only if the integration suite grows substantially.

## Production code

### Keep: `doublestar`

[`github.com/bmatcuk/doublestar/v4`](https://github.com/bmatcuk/doublestar) already owns the nontrivial glob semantics. Its `Match` API accepts absolute or relative slash-separated names, requires a full-name match, and implements recursive `**` components; `ValidatePattern` supports validating user input before it is stored. This is exactly the reusable logic in the feature and avoids a local glob implementation.

It does not, and should not, own listener discovery or forwarding lifecycle.

### Adopt: `sync.WaitGroup.Go`

The module declares Go 1.26.6, so it can use [`sync.WaitGroup.Go`](https://pkg.go.dev/sync#WaitGroup.Go). `Go` starts a task and removes it from the group when the function returns. The standard-library contract also permits a task to call `Go` while the group is nonempty.

That last guarantee fits the rapid-reappearance path: `forwardStopped` can start the replacement worker before the old task returns, so `Close` cannot observe an empty group between the two generations. This replaces the manual `Add`/`go`/`defer Done` bookkeeping without changing the Manager's domain model or adding a dependency.

Expected benefit: a small line-count reduction, but a meaningful reduction in lifecycle bookkeeping and ordering comments.

### Do not adopt now: Suture

[`github.com/thejerf/suture/v4`](https://pkg.go.dev/github.com/thejerf/suture/v4) is the closest mature supervisor library. A service implements `Serve(context.Context) error`; the supervisor can [add services while running](https://pkg.go.dev/github.com/thejerf/suture/v4#Supervisor.Add), [remove them](https://pkg.go.dev/github.com/thejerf/suture/v4#Supervisor.Remove), wait for removal with [`RemoveAndWait`](https://pkg.go.dev/github.com/thejerf/suture/v4#Supervisor.RemoveAndWait), restart failures, and apply failure backoff.

It would take over some mechanics:

- creating and cancelling per-port contexts;
- waiting for child services at shutdown;
- restarting a failed `Forward` call;
- detecting restart thrashing.

It would not remove:

- the desired-port calculation;
- remembered-port precedence;
- the port-to-running-service map, now storing `ServiceToken` values;
- forwarding status generation and ready/error callbacks;
- the disappear/reappear handoff.

In particular, `Remove` returns before the old service has stopped, while adding a replacement starts it immediately. Preserving the current no-overlap behavior still requires either `RemoveAndWait` or generation-aware handoff logic. `RemoveAndWait` would make reconciliation blocking. Suture's failure policy also differs from the current fixed delay: it restarts ordinary failures and only enters a configurable supervisor backoff after a failure threshold. Adapting its `Spec` and event hook would replace explicit code with policy configuration rather than eliminate the state machine.

Recommendation: reconsider Suture only if the application later supervises several kinds of independently restartable long-running services. For one observer plus keyed forwarding workers, it is more abstraction than simplification.

### Do not adopt: generic task and retry libraries

[`golang.org/x/sync/errgroup`](https://pkg.go.dev/golang.org/x/sync/errgroup) models subtasks belonging to one operation; with `WithContext`, the first non-nil task error cancels the group. The Manager instead requires failures and cancellation to be isolated per port, followed by independent retry. It could replace a `WaitGroup`, but not the worker map or lifecycle rules, and `WaitGroup.Go` is a closer zero-dependency fit.

[`github.com/oklog/run`](https://github.com/oklog/run) runs a group of actors until one exits, then interrupts all actors. Its documented use case is coordinating a fixed application lifecycle from `main`; stopping every forward because one worker exits is the opposite of the required behavior.

[`github.com/cenkalti/backoff/v5`](https://pkg.go.dev/github.com/cenkalti/backoff/v5) provides context-aware `Retry`, constant and exponential policies, and retry notifications. It could replace the short retry loops in `observe` and `runForward`, but it does not manage keyed workers, reconciliation, ready callbacks, or forwarding status. Wiring notifications and terminal context errors would save little or no code.

Kubernetes [`controller-runtime/reconcile`](https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/reconcile) uses the same level-based desired-versus-actual concept, but its APIs are designed around Kubernetes objects, caches, controllers, and work queues. The pattern validates the current design; the library is not a sensible dependency for this standalone process.

## Unit tests

### Consider later: `testing/synctest`

[`testing/synctest`](https://pkg.go.dev/testing/synctest) has been part of the standard library since [Go 1.25](https://go.dev/doc/go1.25#synctest). It runs a test and its goroutines inside a bubble with virtual time; `synctest.Wait` waits until the other bubble goroutines are durably blocked.

The core Manager tests are a strong fit because their fake backend communicates only through in-bubble channels and contexts. They can use `synctest.Wait` followed by direct assertions instead of:

- the local `eventually` polling helper;
- one-second `time.After` guards;
- 20 ms negative-event sleeps;
- a 5 ms test-only retry delay;
- scheduler-dependent timing in the rapid-reappearance test.

This removes test-only polling logic and makes lifecycle tests deterministic without a dependency. However, each current test must also be placed inside a synctest bubble. A trial conversion added more test structure than it removed, so the branch keeps its small local polling helpers. Care is still needed for a fake operation that fails forever: virtual time can repeatedly advance its retry timer, so each test must eventually block or cancel.

`synctest` is not suitable for the real SSH integration test. The Go documentation explicitly notes that real network I/O and external processes are not durably blocking, even for loopback connections.

### Do not add an assertion framework just for eventual checks

[`testify/require.Eventually`](https://github.com/stretchr/testify/blob/master/require/require.go) and [Gomega's `Eventually`/`Consistently`](https://pkg.go.dev/github.com/onsi/gomega#Eventually) are mature APIs, but they retain wall-clock polling. They would replace a small helper while adding a testing style and dependency across an otherwise standard-library test suite. `synctest` is the better core-test solution.

## SSH and Docker integration tests

### Conditional: `gotest.tools/v3/poll`

[`gotest.tools/v3/poll`](https://pkg.go.dev/gotest.tools/v3/poll) provides `WaitOn`, timeout/delay settings, diagnostic results, and a connection check. It could standardize the real 15-second SSH/listener polling loops, where virtual time is unavailable.

For the present feature, the benefit is modest: the same result can be achieved with one local integration helper, and starting/stopping the remote process remains test-domain code. Adopt `poll` only if more asynchronous integration scenarios accumulate and consistent timeout diagnostics become a repeated need.

### Do not migrate to Testcontainers-Go for this feature

[Testcontainers-Go's Compose module](https://golang.testcontainers.org/features/docker_compose/) can start and stop a stack, apply [container wait strategies](https://golang.testcontainers.org/features/wait/introduction/), expose service containers, and arrange cleanup. It would be a legitimate option for a separate, deliberate rewrite of the whole shell integration harness.

It does not simplify this branch specifically. The test would still need to:

- generate and trust SSH credentials and an SSH config;
- execute the listener through SSH so the product observes the real remote process and working directory;
- retain and kill the remote PID;
- assert forwarding appearance and disappearance;
- preserve the harness's Docker-context and loopback-isolation checks.

The Compose module also embeds the Compose v2 implementation and configures a Ryuk cleanup container. Introducing that stack to support one new lifecycle test would enlarge the dependency and operational surface more than it reduces feature code.

## Recommended follow-up

Make only these simplifications now:

1. Replace the Manager's manual `WaitGroup.Add`/goroutine/`Done` sequences with `WaitGroup.Go`.
2. Keep the focused Manager lifecycle tests and their small channel/polling helpers.
3. Leave the external SSH/Docker integration path intact and extract one local status-polling helper.

This keeps the essential reconciliation visible, removes avoidable concurrency-test machinery, and introduces no new dependency beyond the already appropriate glob library.
