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

Release Please maintains a release PR from Conventional Commit messages. The
PR updates `CHANGELOG.md` and the default `buildVersion`; merging it creates
the version tag and GitHub Release. The tag-triggered GoReleaser workflow then
builds macOS and Linux archives for AMD64 and ARM64 and updates the Homebrew
tap.

The workflows require one repository secret named
`RELEASE_AUTOMATION_TOKEN`. Create a fine-grained personal access token limited
to `ssh-forward` and `homebrew-ssh-forward`, grant Contents, Issues, and Pull
requests read/write access, then save it under **Settings → Secrets and
variables → Actions**. This lets a Release Please tag trigger the release
workflow and lets GoReleaser update the separate Homebrew tap.

To exercise the packaging locally without publishing:

```bash
goreleaser release --snapshot --clean
```

Do not create release tags manually. Merge the release PR only after its CI is
green. Conventional `fix:` commits produce patches, `feat:` commits produce
minor releases, and `BREAKING CHANGE` remains a minor bump before v1.0.0.
