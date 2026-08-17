# Repository and releases

## Product identity

The greenfield product, repository, and eventual executable are named `ssh-forward`. During development, binaries run only from build directories or the signed desktop bundle and are never installed over the pre-existing command. The legacy utility and its repository remain unrelated and untouched.

## Repository

The personal project root is a new local Git repository with no inherited history or tag (created at implementation start; the design documents were captured in the first commits). A private GitHub remote (`wangnan0916/ssh-forward`) was created later to host the CI workflow: hosted macOS and Linux runners run the unit and Docker-integration jobs in parallel. The complete macOS-local-plus-Docker combination is exercised on the development machine itself — hosted macOS runners cannot nest virtualization, so the disposable-container suite runs on the Linux runner there.

```text
/
├── cli/             # Go Manager module (present)
│   ├── go.mod
│   ├── cmd/         # planned CLI entry (slice 6), landing on app.NewManager
│   └── internal/
├── desktop/         # planned native macOS app (later phase)
├── schema/          # planned versioned config/policy schema (Policy slice)
├── scripts/         # integration harness (present)
├── test/            # protocol fixtures + disposable-host image (present)
├── docs/
├── CONTEXT.md
└── README.md
```

No legacy source directory is included. The transport spike ran on a separate throwaway branch; only its verdict and resulting decision changes returned to `main`.

## Versions

The product begins at `v0.1.0-alpha.1`. The standalone CLI/core and desktop bundle share one product version during the pre-1.0 period, and the desktop embeds a matching core. IPC, configuration, and observation protocol versions remain independent from the product release version.
