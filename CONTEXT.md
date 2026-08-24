# Domain language

- **Development Host**: the remote Linux machine, referenced only by a literal
  OpenSSH `Host` alias.
- **Remote Listener**: a TCP socket reachable at `127.0.0.1:PORT` on the
  Development Host. This includes IPv4 loopback plus same-user IPv4 wildcard
  and dual-stack IPv6 wildcard sockets, but not IPv6-only sockets.
- **Remembered Port**: persistent intent to keep one remote port forwarded.
- **Working Directory Rule**: persistent, host-scoped absolute glob pattern
  matched against a Remote Listener's observed working directory. `**`
  matches across directory levels.
- **Automatic Forward**: a Forward that exists only while a Remote Listener
  matches a Working Directory Rule.
- **Forward**: one system OpenSSH `-L` process that keeps
  `127.0.0.1:PORT` available locally for a Remembered Port or an Automatic
  Forward.
- **Available Port**: an observed Remote Listener that has no Forward.
  Process name and working directory are best-effort volatile metadata.
- **Discovery**: the live remote scan, in `connecting`, `active`, or `failed`
  state.
- **Manager**: one background process per OS user. It observes one Development
  Host, keeps its Remembered Ports forwarded, and reconciles Automatic
  Forwards from Working Directory Rules.
