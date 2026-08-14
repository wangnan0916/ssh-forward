# Use local JSON-RPC 2.0 IPC

CLI and desktop clients will communicate with the local manager through Unix domain sockets on macOS/Linux and named pipes on Windows. The Go Adapter will use `creachadair/jrpc2` with bounded newline framing for generic JSON-RPC 2.0 machinery, while project-owned methods define version negotiation, idempotency, typed errors, and Snapshot Watch/resync semantics; Swift uses a small Foundation `Codable` peer. Endpoint permissions restrict access to the current user, avoiding network exposure while keeping wire types separate from the Manager Interface.
