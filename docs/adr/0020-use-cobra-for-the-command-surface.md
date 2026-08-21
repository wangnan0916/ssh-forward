# Use Cobra for the command surface

The CLI grew nested commands and needed the help shape a mainstream tool teaches: a command list, `ssh-forward <command> --help`, and flags that may sit before or after the positional port. Hand-rolled `flag.FlagSet` dispatch plus a custom interspersed-flag parser duplicated that work and still left help as a separate string. Cobra now owns the command tree and generated help. Composition (host naming, auto-spawn, OpenSSH) lives in `app`, which the CLI calls; tests still run `App.Run` against a fake Manager and skip Connect.

## Considered Options

- Keep Go `flag` and maintain help by hand — rejected: the help page and the command tree drifted, and interspersed flags were a second parser.
- Move host resolution and auto-spawn into the CLI package — rejected: that package would then depend on IPC and OpenSSH, and tests that inject a Manager would have to opt out of a heavier startup path.
