# Architecture

The product contract is:

> Select one SSH alias, see TCP listeners reachable through its IPv4 loopback,
> and keep remembered remote-to-local forwards plus live listeners matching
> configured working-directory globs available on localhost.

Anything that does not serve this sentence is outside the current design.

## Modules

```text
CLI
 ├─ config.jsonc (default host, remembered forwards, and directory rules)
 └─ GET /v1/status over a user-only Unix socket
                         │
                      Manager
               ┌─────────┴─────────┐
        observe listeners      one worker per desired Forward
               │                    │
      ssh HOST sh -s       OpenSSH -O forward/cancel
               └──────── shared OpenSSH master ────────┘
```

- `internal/core` owns the small state machine. Its external interface is
  `Status`, `UpdateIntent`, and `Close`; its true-external backend has
  `Observe`, `Forward`, and `Close`.
- `internal/openssh` owns the product-private multiplexed OpenSSH connection,
  dynamic forwarding commands, process invocation, the fixed remote scanner,
  readiness checks, and bounded SSH error classification.
- `internal/app` owns the single config file, SSH alias selection, and the thin
  adapters that compose HTTP/Unix Socket and the user's OS service manager.
  A Manager loads forwarding intent at startup and accepts idempotent intent
  updates over its user-only Unix socket. It reconciles only affected workers;
  `Connect` restarts it when the selected host, protocol, or binary version
  differs from the current process. `Uninstall` removes only the background
  service; persistent intent remains user-owned configuration.
- `internal/statusview` owns human status grouping, terminal-width fitting,
  missing-value presentation, and optional ANSI styling and hyperlinks. JSON
  bypasses it.
- `internal/cli` owns command orchestration and edits remembered forwards; it
  delegates human status rendering through the `statusview.Render` seam.

Mechanisms are delegated to deep external modules: system OpenSSH handles SSH,
`kardianos/service` handles resident process lifecycle, `net/http` handles local
IPC, `ssh_config` parses Host declarations, and `hujson` parses JSONC. Product
code keeps only their composition and the forwarding state machine. Lip Gloss
renders human status tables, while `x/term` detects stdout capabilities.

## State

Persistent state is only:

- one optional default SSH alias;
- a sorted remembered remote-to-preferred-local mapping list, including its
  fallback policy, per alias;
- a sorted absolute working-directory glob list per alias.

Volatile state is rebuilt after restart:

- current remote listeners;
- best-effort listener executable names and working directories;
- ports currently selected by working-directory rules;
- each active Forward's actual local port, which may temporarily differ from
  its preferred port;
- discovery health;
- each forward's starting, active, or failed state.

The manager retries discovery and failed forwards. A worker always exists for
every Remembered Forward. It creates a worker for a listener whose observed
working directory matches a configured glob, and cancels that Automatic
Forward when a later complete listener snapshot no longer matches. Remembered
intent wins when both sources select the same Remote Port, so only one worker
exists. Changing a Remembered Forward's preferred Local Port or fallback policy
restarts only that worker. Any Forward with fallback enabled may try up to 20
higher ports after a local conflict; implicit same-port and Automatic Forwards
enable that policy by default. The selected port remains volatile. Invalid
configuration prevents a new Manager from starting.
