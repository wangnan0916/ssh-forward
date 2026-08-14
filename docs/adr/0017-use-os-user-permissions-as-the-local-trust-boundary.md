# Use OS user permissions as the local trust boundary

Manager IPC and product files will rely on current-user Unix permissions or Windows ACLs rather than an additional application token. Local listeners and the SSH SOCKS endpoint remain loopback-only, unsafe ownership or symlinks fail closed, and system SSH is launched by absolute path without a local shell. This keeps the local security model small and auditable while explicitly accepting that another process already running as the same user is inside the trust boundary.
