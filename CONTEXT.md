# Domain language

- **Development Host**: the remote Linux machine, referenced only by a literal
  OpenSSH `Host` alias.
- **Remote Listener**: an IPv4 TCP socket listening on `127.0.0.1:PORT` on the
  Development Host.
- **Remembered Port**: persistent intent to keep one remote port forwarded.
- **Forward**: one system OpenSSH `-L` process that keeps
  `127.0.0.1:PORT` available locally for a Remembered Port.
- **Available Port**: an observed Remote Listener that is not remembered.
- **Discovery**: the live remote scan, in `connecting`, `active`, or `failed`
  state.
- **Manager**: one background process per OS user. It observes one Development
  Host and keeps its Remembered Ports forwarded.

Avoid using “policy”, “rule”, “managed forward”, “allocated port”, or
“process evidence”: those concepts are not part of the current product.
