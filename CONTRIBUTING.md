# Contributing

Keep changes inside the one-sentence contract in [ARCHITECTURE.md](ARCHITECTURE.md).

## Checks

```bash
cd cli
go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
go mod tidy -diff
go test -run '^$' -bench . -benchmem ./internal/core ./internal/openssh
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

## Releases

Release tags and the default `buildVersion` use the same semantic version.
GoReleaser builds macOS and Linux archives for AMD64 and ARM64:

```bash
goreleaser release --snapshot --clean
```

Push a release tag only after `main` is green. Tags containing a prerelease
suffix such as `-alpha.1` become GitHub prereleases automatically.
