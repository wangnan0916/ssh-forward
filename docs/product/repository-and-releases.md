# Repository and releases

## Product identity

The greenfield product, repository, and eventual executable are named `ssh-forward`. During development, binaries run only from build directories or the signed desktop bundle and are never installed over the pre-existing command. The legacy utility and its repository remain unrelated and untouched.

## Repository

After the design session reaches shared understanding and the user explicitly authorizes implementation, the personal project root will become a new local Git repository with no inherited history, tag, or remote. Creating or pushing a remote is a separate future decision.

```text
/
├── cli/
│   ├── go.mod
│   ├── cmd/ssh-forward/
│   └── internal/
├── desktop/
├── schema/
├── docs/
├── CONTEXT.md
└── README.md
```

No legacy source directory is included. The first commit captures the accepted design documents before formal code. The transport spike ran on a separate throwaway branch; only its verdict and resulting decision changes returned to `main`.

## Versions

The product begins at `v0.1.0-alpha.1`. The standalone CLI/core and desktop bundle share one product version during the pre-1.0 period, and the desktop embeds a matching core. IPC, configuration, and observation protocol versions remain independent from the product release version.
