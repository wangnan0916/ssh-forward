# Contributing

Keep changes inside the one-sentence contract in [ARCHITECTURE.md](ARCHITECTURE.md).
Prefer deleting a requirement over adding a framework for a hypothetical
client.

## Checks

```bash
cd cli
go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
go mod tidy -diff
```

The real OpenSSH path uses a disposable Linux container and never connects to
a developer's configured hosts:

```bash
./scripts/test-integration
```

Behavior tests should use `core.Manager` (`Status`, `Close`) or its `Backend`
interface. Keep HTTP/Unix Socket coverage to one status round trip and do not
retest third-party parsing or OS service internals. Do not duplicate OpenSSH
forwarding behavior in Go.
