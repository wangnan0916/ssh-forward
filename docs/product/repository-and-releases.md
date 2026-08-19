# Repository and releases

## Product identity

The product, repository, and CLI executable are named `ssh-forward`. The Go module path is `github.com/wangnan0916/ssh-forward/cli`.

## Repository

The project lives at `github.com/wangnan0916/ssh-forward`. Hosted macOS and Linux runners run the unit and Docker-integration jobs in parallel. Hosted macOS runners cannot nest virtualization, so the disposable-container suite runs on the Linux runner; the complete macOS-local-plus-Docker combination is exercised on a development machine.

```text
/
├── cli/             # Go Manager module
│   ├── go.mod
│   ├── cmd/ssh-forward
│   ├── integration/ # disposable-host suites (integration build tag)
│   └── internal/
├── .agents/skills/  # usage skill for npx skills / coding agents
├── scripts/         # integration harness and Homebrew formula snapshot
├── test/            # protocol fixtures + disposable-host image
├── docs/
├── CONTEXT.md
├── README.md
├── LICENSE
├── SECURITY.md
└── CONTRIBUTING.md
```

A loopback WebUI is in the CLI (`ssh-forward ui start`); a native macOS app (`desktop/`) follows once the WebUI is ready. The `desktop/` directory does not exist yet. No TUI is planned (ADR-0021). No legacy source directory is included. The transport spike ran on a separate throwaway branch; only its verdict and resulting decision changes returned to `main`.

## Versions

The first public CLI is `v0.1.0-alpha.1`. The standalone CLI/core, a future WebUI, and a future desktop bundle share one product version during the pre-1.0 period. IPC, configuration, and observation protocol versions remain independent from the product release version.

The earlier `v0.1.0` tag predates the public module path and should not be used as an install source. Cut `v0.1.0-alpha.1` (or later) from a commit that already uses `github.com/wangnan0916/ssh-forward/cli`.

Until that tag exists, install from `main`: `go install …@main`, or `brew install --HEAD wangnan0916/ssh-forward/ssh-forward` (tap [`wangnan0916/homebrew-ssh-forward`](https://github.com/wangnan0916/homebrew-ssh-forward); keep it in sync with `scripts/homebrew/ssh-forward.rb`).
