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
- The remote command is a fixed embedded script. It reads only
  `/proc/net/tcp` and returns loopback port numbers.
- Every local forward binds explicitly to `127.0.0.1`.
- The manager socket and state files are accessible only to the current OS
  user.
- Remote scanner frames, stderr retention, and observed listener counts are
  bounded.

The manager can expose a remote service to other processes running as the same
local user. Remember only ports you intend to make locally reachable.
