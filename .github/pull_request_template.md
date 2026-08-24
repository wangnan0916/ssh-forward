## What changed

Use a Conventional Commit title, such as `feat: add status colors` or
`fix(discovery): identify unnamed listeners`.

Describe the user-visible behavior and why it belongs in ssh-forward's focused
product contract.

## Verification

- [ ] `go test ./...`
- [ ] `go test -race ./...`
- [ ] `go vet ./...`
- [ ] Documentation reflects user-visible changes
