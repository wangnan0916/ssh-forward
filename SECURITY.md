# Security Policy

This project is pre-1.0. Please report vulnerabilities against `main`.

## Reporting a vulnerability

Use [GitHub Security Advisories](https://github.com/wangnan0916/ssh-forward/security/advisories/new) so the report stays private until a fix is ready.

Do not open a public issue, pull request, or discussion for a vulnerability.

Please include:

- The `ssh-forward --version` string
- The Local Machine OS and the Development Host OS
- Steps to reproduce, or a minimal proof of concept
- The impact you expect (for example: unexpected bind, process metadata leak, manager takeover)

## Trust boundary

`ssh-forward` uses the current OS user and system OpenSSH as its local trust boundary. It does not store SSH credentials. Local Endpoints and the SSH SOCKS reservation bind only to loopback.

See [docs/security/threat-model.md](docs/security/threat-model.md) for the full model, including guarantees that are not yet enforced.
