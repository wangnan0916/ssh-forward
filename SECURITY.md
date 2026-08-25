# Security policy

Report vulnerabilities privately through
[GitHub Security Advisories](https://github.com/wangnan0916/ssh-forward/security/advisories/new).

Please include the version, local OS, Development Host OS, reproduction steps,
and expected impact.

## Trust boundary

- SSH authentication, keys, host verification, jump hosts, and proxy settings
  belong to system OpenSSH. This project stores no credentials.
- Host aliases and ports are passed as an argument vector to an absolute `ssh`
  executable; no local shell interpolates them.
- The remote command is a fixed embedded script. It reads TCP listener state,
  the IPv6 bind mode, and best-effort executable/working-directory links from
  procfs. When available, `ss` associates sockets with same-user processes.
- Remote process metadata is bounded, UTF-8 repaired, and stripped of terminal
  control characters before it reaches status output. It is never persisted.
- Every Forward uses explicit `127.0.0.1` endpoints. Imported services listen
  on local loopback. After creating a Published Forward, the Adapter verifies
  the actual Development Host socket through procfs before reporting it active;
  wildcard binds forced by `GatewayPorts yes` are canceled and reported. If
  cancellation of an installed forward fails, the product-owned SSH master is
  stopped so its listeners cannot remain reachable.
- The manager socket and state files are accessible only to the current OS
  user.
- Remote scanner frames, stderr retention, and observed listener counts are
  bounded.

The Manager can expose a remote service to local processes, or a local service
to processes and users on the Development Host. Loopback is machine-local, not
user-private. Configure only ports you intend to make reachable in that scope.
TCP forwarding adds no application authentication; the forwarded service's own
authentication and trust model still apply. The local Manager can connect to a
configured Local Service even when that service is otherwise reachable only on
local loopback.

Browser debugging endpoints grant control over their browser profile. Use a
dedicated profile without sensitive browsing, publish it only to a trusted
single-user Development Host, and unpublish it as soon as it is no longer
needed.

Published Forwards require sshd `GatewayPorts no` (the default) or
`GatewayPorts clientspecified`. `GatewayPorts yes` forces wildcard listeners;
ssh-forward fails closed if it detects that policy or cannot verify the actual
remote bind.
