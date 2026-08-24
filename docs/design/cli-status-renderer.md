# CLI status renderer

Status: implemented

## Problem

Human-readable `ssh-forward status` output is assembled with ad hoc string
concatenation. Ports align, but the remaining fields do not form stable
columns, missing listener metadata disappears silently, and long working
directories can dominate the terminal. Styling this code in place would mix
terminal policy into command execution and make `--json` easier to disturb.

## Outcome

Render human status as compact, borderless tables with explicit columns:

```text
Host  ubuntu    Discovery  active

FORWARDS
REMOTE  TARGET           KIND        APP   WORKING DIRECTORY
  5173  127.0.0.1:15173  remembered  —     —
 12000  127.0.0.1:12000  automatic   node  …/console.cli.im

AVAILABLE
 PORT  APP           WORKING DIRECTORY
  631  —             —
 7897  verge-mihomo  /home/shampoo
33331  clash-verge   /home/shampoo
```

The output remains readable without color. Forward Remote Ports use a
six-character right-aligned `REMOTE` column; Available Ports retain the
five-character right-aligned `PORT` column. Unavailable metadata appears as
`—`.

## Module seam

Add a deep `internal/statusview` module with one interface:

```go
func Render(w io.Writer, status core.Status, options Options) error

type Options struct {
    Width      int
    Color      bool
    Hyperlinks bool
}
```

The module owns all human status presentation:

- grouping forwards by state;
- mapping diagnostics to human text;
- selecting headings and columns;
- alignment and spacing;
- missing-value placeholders;
- working-directory truncation;
- optional ANSI styling.
- terminal hyperlinks for active local targets.

The CLI adapter derives `Options` from stdout. It detects TTY state and width
with `golang.org/x/term`; color and hyperlinks are enabled only for a TTY when
`NO_COLOR` is unset. A non-file writer, failed size probe, or redirected stdout
produces plain, unbounded text.

`core` does not import or refer to the renderer. Manager IPC serializes the
complete `core.Status` model. Public `status --json` output bypasses the
renderer and uses a CLI-owned compatibility projection: same-port Forwards
retain the legacy `port` field, while mappings whose Preferred Local Port or
actual Local Port differs from the Remote Port expose the complete port model.
The compatibility projection does not affect Manager IPC.

## Rendering rules

1. Use `charm.land/lipgloss/v2` and its table package. Do not add Bubble Tea,
   `go-pretty`, borders, animation, or interactive navigation.
2. Render `Host` and `Discovery` as a compact summary before any sections.
3. Render active, starting, failed, and available rows in separate sections.
   Preserve the existing headings `FORWARDS`, `STARTING`, and
   `NEEDS ATTENTION`; diagnostics remain visible for failed rows.
4. Align Forward Remote Ports to the right in a six-cell `REMOTE` column and
   Available Ports to the right in a five-cell `PORT` column. Align all other
   columns to the left.
5. Render an absent app or working directory as `—`. Do not infer that missing
   metadata belongs to a system process.
6. Identify every Forward as `remembered` or `automatic`; meaning must not
   depend on whether matching listener metadata is currently available.
7. In a width-constrained TTY, preserve the remote port, target, kind, and app
   columns and shorten only the working-directory column from the left with a
   single `…`. Preserve the final path segment. Do not truncate redirected
   output.
8. Use a restrained semantic palette: bright green for active
   discovery/forwards, bright yellow for connecting/starting, bright red for
   failures, bright cyan for available ports, bright magenta for app names,
   cyan for targets, and muted gray for table headers and working directories.
   Meaning must never depend on color alone.
9. `status --watch` continues to append changed snapshots. It does not clear
   the terminal or become a full-screen TUI.
10. `Connecting to HOST...` remains a stderr progress message and is outside
   the table.
11. In an ANSI-enabled TTY, active forward targets use OSC 8 hyperlinks to
    `http://127.0.0.1:PORT`. Starting and failed targets remain plain text, as
    does all piped or redirected output.

## Compatibility

- Existing statuses without Working Directory Rules retain their JSON shape;
  configured rules add `working_directory_rules`, and Automatic Forwards add
  `automatic: true`.
- Piped and redirected human output contains no ANSI escape sequences.
- `NO_COLOR` disables ANSI styling.
- macOS and Linux remain supported on AMD64 and ARM64.
- Listener discovery policy is unchanged. In particular, whether root-owned
  loopback listeners such as CUPS on port 631 should be listed is a separate
  product decision.

## Verification

Add renderer tests at the module interface for:

- active forwards and available listeners in aligned columns;
- right alignment of 3-, 4-, and 5-digit ports in both port columns;
- `—` for each missing metadata combination;
- all forward states and diagnostics;
- path shortening at a fixed narrow width;
- full paths with `Width == 0`;
- ANSI enabled and disabled independently of content;
- an empty active discovery result.

Keep one CLI integration test proving that human `status` delegates to the
renderer, and retain the existing JSON tests. Run the repository checks from
`CONTRIBUTING.md`, including unit, race, vet, formatting, module tidiness,
benchmarks, and the disposable Docker integration suite.

## Acceptance criteria

- The example structure is represented in the plain-text test fixture.
- Columns remain aligned when port widths and metadata presence differ.
- A long working directory cannot push a TTY row past the detected width.
- Unknown metadata is visible but is not mislabeled.
- No ANSI escapes appear in a pipe, redirect, `NO_COLOR` mode, or JSON.
- Existing commands and persistent configuration behave unchanged.
