# Domain language

- **Development Host**: the remote Linux machine, referenced only by a literal
  OpenSSH `Host` alias.
- **Remote Listener**: an IPv4 TCP socket listening on `127.0.0.1:PORT` on the
  Development Host.
- **Remembered Port**: persistent intent to forward one remote port when it is
  listening.
- **Forward**: one system OpenSSH `-L` process mapping
  `127.0.0.1:PORT` locally to the same endpoint remotely.
- **Available Port**: an observed Remote Listener that is not remembered.
- **Waiting Port**: a Remembered Port with no current Remote Listener.
- **Discovery**: the live remote scan, in `connecting`, `active`, or `failed`
  state.
- **Manager**: one background process per OS user. It observes one Development
  Host and supervises its Forwards.

Avoid using “policy”, “rule”, “managed forward”, “allocated port”, or
“process evidence”: those concepts are not part of the current product.
