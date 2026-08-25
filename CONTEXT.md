# Domain language

- **Development Host**: the remote Linux machine, referenced only by a literal
  OpenSSH `Host` alias.
- **Remote Listener**: a TCP socket reachable at `127.0.0.1:PORT` on the
  Development Host. This includes IPv4 loopback plus same-user IPv4 wildcard
  and dual-stack IPv6 wildcard sockets, but not IPv6-only sockets.
- **Remote Port**: the port number of a Remote Listener or remembered endpoint
  on the Development Host.
- **Preferred Local Port**: the persistent first-choice local loopback port for
  a Forward.
- **Local Port**: the port actually bound on local loopback by a Forward. It may
  temporarily differ from both the Remote Port and Preferred Local Port.
- **Local Service**: an explicitly configured TCP endpoint reachable at
  `127.0.0.1:PORT` on the local machine. It is the target of a Published
  Forward and is not discovered by the Manager.
- **Published Port**: the strict TCP port bound at `127.0.0.1:PORT` on the
  Development Host for a Published Forward.
- **Remembered Forward**: persistent intent to map one Remote Port to a
  Preferred Local Port, with or without temporary fallback.
- **Working Directory Rule**: persistent, host-scoped absolute glob pattern
  matched against a Remote Listener's observed working directory. `**`
  matches across directory levels.
- **Automatic Forward**: a Forward that exists only while a Remote Listener
  matches a Working Directory Rule. Its Preferred Local Port equals its Remote
  Port and it allows temporary fallback.
- **Published Forward**: persistent intent to map one Local Service to one
  Published Port through OpenSSH remote forwarding. It never falls back to a
  different Published Port.
- **Forward Direction**: `remote_to_local` for Remembered and Automatic
  Forwards, or `local_to_remote` for Published Forwards.
- **Forward**: one live directional mapping between a local loopback port and
  a Development Host loopback port, implemented through the shared OpenSSH
  connection.
- **Available Port**: an observed Remote Listener that has no Forward.
  Process name and working directory are best-effort volatile metadata.
- **Discovery**: the live remote scan, in `connecting`, `active`, or `failed`
  state.
- **Manager**: one background process per OS user. It observes one Development
  Host, keeps its Remembered and Published Forwards active, and reconciles
  Automatic Forwards from Working Directory Rules.
