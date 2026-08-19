# Go TUI library options for `ssh-forward`

**Superseded.** Slice 7 is a loopback WebUI, not a TUI ([ADR-0021](../adr/0021-serve-a-loopback-webui-instead-of-a-tui.md), [webui.md](../product/webui.md)). This file is the 2026-08-18 Bubble Tea survey kept for history. Do not add Charm libraries.

_Original scope: a presentation-only TUI over the Manager Interface. Evidence checked against upstream repositories, licenses, `go.mod` files, pkg.go.dev, the Go module proxy, and Charm first-party docs on 2026-08-18._

## Recommendation (historical)

~~When slice 7 starts, implement the TUI on Charm Bubble Tea v2 (`charm.land/bubbletea/v2`), with `charm.land/bubbles/v2` for list/viewport/help/key and `charm.land/lipgloss/v2` for layout and styling.~~ **Do not implement a TUI.** The next paragraph is leftover survey context.

This maps cleanly onto the planned product: a full-screen alt-screen live list, grouped more simply than the desktop Dashboard (Needs Attention / Active / Available), with keyboard actions for remember/forget and quit. Watch delivers coalesced **full Snapshots**, not patches; the Elm `Msg` loop is the right place to replace presentation state from a client goroutine. Bubble Tea v2 is a tagged stable major (v2.0.0 on 2026-02-24, latest **v2.0.8** on 2026-07-03), MIT-licensed, and already used with Cobra in first-party Charm apps. The project's Go 1.26.6 toolchain satisfies Bubble Tea v2's Go 1.25.0 floor ([v2.0.8 `go.mod`](https://raw.githubusercontent.com/charmbracelet/bubbletea/master/go.mod), [proxy info](https://proxy.golang.org/charm.land/bubbletea/v2/@v/v2.0.8.info), [LICENSE](https://raw.githubusercontent.com/charmbracelet/bubbletea/master/LICENSE)).

Adopt:

- `charm.land/bubbletea/v2` — program loop, alt screen as a `View` field, `Program.Send` from the Watch goroutine, `WithContext` for Cobra cancellation, `Quit` / `Interrupt`.
- `charm.land/bubbles/v2` — `list` and/or `viewport` plus `help` and `key`. Skip file picker, timer, stopwatch, and fancy list chrome the product does not need.
- `charm.land/lipgloss/v2` — styles, flex-like layout, optional `lipgloss/v2/table` for row rendering. Color downsampling is automatic when Lip Gloss is used under Bubble Tea ([Lip Gloss README](https://raw.githubusercontent.com/charmbracelet/lipgloss/master/README.md)).

Leave out unless a later TUI slice proves the gap:

- `charm.land/huh/v2` — forms/prompts, not a live Watch surface. The CLI already has Cobra flags for host and `add`/`remove`; remember/forget from a selected row does not need a multi-page form.
- `github.com/charmbracelet/harmonica` — spring animation; last tag **v0.2.0** (2022-04-15). Irrelevant to Snapshot lists.
- `charm.land/wish/v2` — SSH **server** for hosting TUIs remotely. ssh-forward's TUI runs on the Local Machine against a local Unix socket.

Pin one tested trio (`bubbletea` / `bubbles` / `lipgloss`) and let `go mod tidy` pull the rest. Do not add a second TUI stack.

## Important boundary

A TUI library owns terminal takeover, restore on exit, keyboard dispatch, and redraw. It does **not** own:

- Manager discovery, auto-spawn, or `app.Connect` / `app.Serve`
- JSON-RPC methods, line framing, `system.hello`, or Watch coalescing/resync
- Policy writes (`add`/`remove` / `policies.jsonc`)
- Host resolution or OpenSSH

ADR-0001 and ADR-0014 already freeze that split: CLI, TUI, and desktop are Adapters over `Snapshot` / `Watch` / `Close`. The TUI must not become a second Manager.

## Evaluation by concern

### 1. Fit to Snapshot / Watch

Watch is a long-lived JSON-RPC subscription that pushes **full, monotonically revised Snapshots** (see `docs/research/ipc-library-options.md` and `docs/design/ipc-protocol.md`). The TUI should replace its view-model from the latest snapshot and keep local UI state (cursor, filter, pending confirm) beside it.

| Stack | Mapping |
|---|---|
| **Bubble Tea** | Custom `snapshotMsg` (or the Snapshot type itself) is a `tea.Msg`. A Watch goroutine calls `p.Send(...)`. `Update` replaces presentation state. First-party example: `examples/send-msg` sends custom msgs from a goroutine via `Program.Send` ([source](https://raw.githubusercontent.com/charmbracelet/bubbletea/main/examples/send-msg/main.go)). `Send` is documented as blocking until the program is ready, and a no-op after exit ([`tea.go` on default branch](https://github.com/charmbracelet/bubbletea/blob/main/tea.go)). |
| **tview** | Widgets are mutated in place. Async work must enter the event loop with `Application.QueueUpdate` / `QueueUpdateDraw`, which run `f` on the UI goroutine and optionally redraw ([`application.go` at v0.42.0](https://raw.githubusercontent.com/rivo/tview/v0.42.0/application.go)). Full-state replace becomes “clear table/list and refill,” which races with focus/cursor unless the app owns that policy. |

Bubble Tea is the better fit: Watch is already a stream of complete states, and Elm messages are full-state replace without fighting widget internals.

### 2. Keyboard-first list + actions

Planned TUI (from `docs/product/mvp.md`): live view of the Development Host, new remote ports, and Active Forwards; remember/forget a port or directory from that view. Grouping should echo desktop **Needs Attention / Active / Available**, simpler than the Dashboard (`docs/product/desktop-experience.md`). Not a multi-host UI, not a policy editor.

Bubbles v2 first-party widgets: spinner, textinput, textarea, **table**, progress, paginator, **viewport**, **list**, filepicker, timer, stopwatch, **help**, **key** ([Bubbles README](https://raw.githubusercontent.com/charmbracelet/bubbles/master/README.md)). A custom model over `list`/`viewport` (or Lip Gloss table + viewport) is enough. Mouse is optional; v2 declares mouse mode on `tea.View` rather than `WithMouseCellMotion()` ([upgrade guide](https://raw.githubusercontent.com/charmbracelet/bubbletea/master/UPGRADE_GUIDE_V2.md)).

tview has richer built-in tables/lists/forms ([tview README](https://raw.githubusercontent.com/rivo/tview/master/README.md)) but pulls the product toward an immediate-mode widget tree that this repo does not need.

### 3. Cobra / existing CLI

The command tree already lives in Cobra (ADR-0020). Root requires a subcommand today (`RootCommand` `RunE` is `missingCommand`). `status`/`watch` take `--json` for scripts. Composition seam is `app.Connect` / `app.Serve` ([`cli/internal/cli/command.go`](../../cli/internal/cli/command.go), [`cli/internal/app/singleton.go`](../../cli/internal/app/singleton.go)).

Bubble Tea `Program.Run()` takes over stdin/stdout until exit; Charm documents that logging must go to a file because the TUI occupies the terminal ([Bubble Tea README](https://raw.githubusercontent.com/charmbracelet/bubbletea/master/README.md)). Cobra composition is a blocking `RunE` that constructs `tea.NewProgram(..., tea.WithContext(cmd.Context()))` and returns after `Run()`. `WithContext` cancels the program with `ErrProgramKilled` ([`options.go` at v2.0.8](https://raw.githubusercontent.com/charmbracelet/bubbletea/v2.0.8/options.go)). `WithoutRenderer()` exists for non-TTY/daemon-like use, which we should **not** use for the interactive TUI, but it confirms the library expects TTY takeover to be optional.

First-party production evidence that Cobra and Bubble Tea v2 coexist in one binary: Glow's module requires both `github.com/spf13/cobra v1.10.2` and `charm.land/bubbletea/v2 v2.0.8` ([Glow `go.mod`](https://raw.githubusercontent.com/charmbracelet/glow/master/go.mod)).

**Product choice:** add an explicit `ssh-forward tui` (name TBD) subcommand. Do not make the TUI the no-arg default until CLI scripts and `missingCommand` behavior are revisited. Never run the TUI when `--json` is set or stdout is not a TTY.

### 4. Testing

This repository is test-heavy. Bubble Tea models are ordinary Go types: `Update(msg) (Model, Cmd)` can be unit-tested with fake Snapshots and key messages and **no TTY**. That should be the default test style, matching how CLI tests inject a fake Manager.

Charm also ships experimental program-level testers:

- `github.com/charmbracelet/x/exp/teatest` — Bubble Tea **v1** (`github.com/charmbracelet/bubbletea v1.3.5` on pkg.go.dev). First-party write-up: [Writing Bubble Tea Tests](https://charm.land/blog/teatest/) (2023-05-08). The `x` repo README states experimental packages have **no compatibility promises** ([`charmbracelet/x` README](https://pkg.go.dev/github.com/charmbracelet/x/exp/teatest)).
- `github.com/charmbracelet/x/exp/teatest/v2` — Bubble Tea **v2**, but the module proxy/pkg.go.dev graph still listed `charm.land/bubbletea/v2 v2.0.0-rc.1` as of this check, not v2.0.8 ([pkg.go.dev teatest/v2](https://pkg.go.dev/github.com/charmbracelet/x/exp/teatest/v2)). Do not take this as a production dependency until a pin matches the app's Bubble Tea version.

v2 also adds `WithColorProfile` and `WithWindowSize` specifically “great for testing” ([upgrade guide](https://raw.githubusercontent.com/charmbracelet/bubbletea/master/UPGRADE_GUIDE_V2.md), [`options.go` v2.0.8](https://raw.githubusercontent.com/charmbracelet/bubbletea/v2.0.8/options.go)).

tview has ordinary Go tests around widgets; there is no Charm-style golden harness. Async `QueueUpdateDraw` tests need a real or fake tcell screen. Termdash advertises testability as a design goal ([termdash README](https://raw.githubusercontent.com/mum4k/termdash/master/README.md)) but is the wrong product shape (see Rejected).

### 5. Footprint, licenses, module graph

Current CLI direct deps: `jrpc2`, `cobra`, `go-cmp` ([`cli/go.mod`](../../cli/go.mod)). The TUI should stay in that spirit: one stack, MIT/Apache/BSD only.

**Bubble Tea v2.0.8 direct require** (from pkg.go.dev / `go.mod`): `colorprofile`, `ultraviolet` (pseudoversion `v0.0.0-20260703014108-f5a850f9c2b7`), `x/ansi`, `x/exp/golden`, `x/term`, `go-colorful`, `cancelreader`, `golang.org/x/sys`. Indirect: `uniseg`, `displaywidth`/`uax29`, `x/termios`, `x/windows`, `terminfo`, `x/sync`. **No tcell.** License: MIT ([LICENSE](https://raw.githubusercontent.com/charmbracelet/bubbletea/master/LICENSE)). Ultraviolet: MIT ([LICENSE](https://raw.githubusercontent.com/charmbracelet/ultraviolet/master/LICENSE)).

**Bubbles v2.1.1** (2026-07-04): MIT; requires Bubble Tea v2, Lip Gloss v2, harmonica, fuzzy, clipboard, uniseg ([pkg.go.dev](https://pkg.go.dev/charm.land/bubbles/v2)). Harmonica comes in even if the TUI never calls it, unless a later trim proves otherwise.

**Lip Gloss v2.0.6** (2026-08-11): MIT ([LICENSE at v2.0.6](https://raw.githubusercontent.com/charmbracelet/lipgloss/v2.0.6/LICENSE)).

**tview v0.42.0**: MIT ([LICENSE.txt](https://raw.githubusercontent.com/rivo/tview/v0.42.0/LICENSE.txt)); requires `tcell/v2 v2.8.1`, `go-colorful`, `uniseg`. **tcell v2.13.10** and **tcell v3.4.1**: Apache-2.0 ([LICENSE at v2.13.10](https://raw.githubusercontent.com/gdamore/tcell/v2.13.10/LICENSE), [LICENSE at v3.4.1](https://raw.githubusercontent.com/gdamore/tcell/v3.4.1/LICENSE)). Apache-2.0 is compatible with the tree; the issue is extra renderer stack, not copyleft.

No GPL/copyleft TUI candidate in this survey needs a hard reject on license grounds. The reject list is fit and maintenance.

### 6. Terminal reality (macOS Terminal, iTerm2, VS Code, Linux)

None of the libraries publish a certification matrix for “VS Code integrated terminal.” Claims below are from library docs, not folklore.

- **Bubble Tea v2** ships a cell-based “Cursed Renderer” (Charm's description of an ncurses-style algorithm) and **built-in color downsampling** via `colorprofile`. Lip Gloss v2 is “pure” (no independent terminal I/O); Bubble Tea owns I/O ([v2.0.0 notes](https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.0), [Charm v2 blog, 2026-02-23](https://charm.land/blog/v2.md)). `WithColorProfile` can force Ascii/ANSI/ANSI256/TrueColor for tests. `colorprofile.Detect` documents TrueColor / ANSI256 / ANSI / Ascii / NoTTY ([colorprofile README](https://raw.githubusercontent.com/charmbracelet/colorprofile/v0.4.3/README.md)). Lip Gloss documents 16-color, 256-color, and truecolor hex, plus automatic downsample ([Lip Gloss README](https://raw.githubusercontent.com/charmbracelet/lipgloss/master/README.md)). Unicode width goes through `uniseg` and `clipperhouse/displaywidth` in the module graph. Lip Gloss's table example explicitly includes CJK and Arabic cells.
- **tcell v3** documents POSIX (Linux, macOS, …), wide characters and grapheme clusters, ANSI/XTerm 256 colors, and 24-bit color via `COLORTERM=truecolor`, `TERM` suffixes `-truecolor`/`-direct`, and `TCELL_TRUECOLOR=disable`. It also documents keyboard/mouse/negotiation escape hatches (`TCELL_KEYBOARD_PROTOCOL`, `TCELL_NEGOTIATE`, `TCELL_MOUSE`) when emulators answer queries badly ([tcell v3 README](https://pkg.go.dev/github.com/gdamore/tcell/v3)). **Bubble Tea v2 does not use tcell**, so those env vars are not the Bubble Tea control surface.

Expect the usual integrated-terminal issues (color profile, alt-screen restore, wide glyphs) to be integration tests on macOS Terminal, iTerm2, VS Code, and a Linux VTE, not something a library README can promise.

### 7. Maintenance and API stability in 2026

| Module | Path | Latest tag seen | Date (proxy) | Stability |
|---|---|---|---|---|
| Bubble Tea v2 | `charm.land/bubbletea/v2` | **v2.0.8** | 2026-07-03 | Tagged v2.0.0 on 2026-02-24; Charm blog: out of beta ([v2.0.0](https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.0), [blog](https://charm.land/blog/v2.md)). Vanity import; GitHub repo remains `charmbracelet/bubbletea`. |
| Bubble Tea v1 | `github.com/charmbracelet/bubbletea` | **v1.3.10** | 2025-09-17 | Still on pkg.go.dev; no newer v1 tag found. New work should not start here. |
| Bubbles v2 | `charm.land/bubbles/v2` | **v2.1.1** | 2026-07-04 | Accompanies Bubble Tea v2. |
| Lip Gloss v2 | `charm.land/lipgloss/v2` | **v2.0.6** | 2026-08-11 | Active. |
| Huh v2 | `charm.land/huh/v2` | **v2.0.3** | 2026-03-10 | Tagged v2 exists; skip for first TUI. Master/`go.mod` may be ahead of the tag. |
| Wish v2 | `charm.land/wish/v2` | **v2.0.3** | 2026-07-31 | Active; wrong product. v1 path `github.com/charmbracelet/wish` last tag v1.4.7 (2025-03-25). |
| tview | `github.com/rivo/tview` | **v0.42.0** | 2025-08-27 | README still says `go get github.com/rivo/tview@master` ([README](https://raw.githubusercontent.com/rivo/tview/master/README.md)). Pre-v1; author documents possible breaks for tcell upgrades and “internal” `Primitive`. Tagged v0.42.0 still depends on **tcell v2.8.1**, while tcell v2 is at **v2.13.10** (2026-05-06). |
| tcell | `github.com/gdamore/tcell/v2` and `/v3` | **v2.13.10**, **v3.4.1** | 2026-05-06 / 2026-07-19 | v3 is current (`go.mod` path `.../tcell/v3`); v1 unmaintained ([tcell v3 README](https://pkg.go.dev/github.com/gdamore/tcell/v3)). Breaking v2→v3. |

**v1 vs v2 for this product:** start on v2. The migration tax (import path, `View() tea.View`, `KeyPressMsg`, alt screen as a field) is cheaper on a greenfield TUI than after shipping v1. Risks: Charm's renderer is now ultraviolet (pseudoversion, not a tagged module as of Bubble Tea v2.0.8); vanity `charm.land` paths; teatest/v2 lagging the stable tag; Bubbles pulling harmonica/clipboard.

### 8. What we would still own

Regardless of library:

1. JSON-RPC client, hello/capabilities, Watch coalescing, resync, bounded latest-snapshot slot.
2. Mapping Snapshot → rows (Needs Attention / Active / Available); IPv4/IPv6 identity rules stay in the domain, not the widget.
3. remember/forget via existing app/CLI policy writers — not a TUI-local policy store.
4. Reconnect/pause only if those operations already exist on the Manager adapter; the TUI must not invent SSH control.
5. Cobra wiring, TTY detection, restoring the terminal so subsequent CLI commands still work.
6. Tests: fake Manager/Watch, golden Snapshot→view, key-driven Update tests.

## Preferred stack (detail)

### Bubble Tea v2 — Elm architecture

Init / Update / View. `View()` returns `tea.View`; set `v.AltScreen = true` for the full-screen live list ([upgrade guide](https://raw.githubusercontent.com/charmbracelet/bubbletea/master/UPGRADE_GUIDE_V2.md)). Concurrent Watch: `p.Send(msg)` ([`tea.go`](https://github.com/charmbracelet/bubbletea/blob/main/tea.go), [send-msg example](https://raw.githubusercontent.com/charmbracelet/bubbletea/main/examples/send-msg/main.go)). Cancellation: `WithContext`. Quit: `tea.Quit` / `Program.Quit()`. SIGINT: `InterruptMsg` / `ErrInterrupted`. Renderer is **ultraviolet**, not tcell (v2 `go.mod`).

### Bubbles + Lip Gloss — widgets and paint

Use list/viewport/help/key. Lip Gloss handles layout, color, and optional tables (CJK included in the upstream example). Do not build a second layout engine.

### Huh, Harmonica, Wish — not for slice 7

Huh is a form kit inspired by Survey ([Huh README](https://raw.githubusercontent.com/charmbracelet/huh/master/README.md)); MIT. Harmonica is springs ([harmonica README](https://pkg.go.dev/github.com/charmbracelet/harmonica)). Wish is an SSH app server with a Bubble Tea middleware ([Wish README](https://pkg.go.dev/charm.land/wish/v2)). ssh-forward already uses system OpenSSH as a **client**.

## Viable but weaker: tview + tcell

tview is the ncurses-like alternative: Flex/Grid, tables, lists, forms, application wrapper, MIT, built on tcell ([README](https://raw.githubusercontent.com/rivo/tview/master/README.md)). Async: `QueueUpdate` / `QueueUpdateDraw` ([v0.42.0 `application.go`](https://raw.githubusercontent.com/rivo/tview/v0.42.0/application.go)). Unicode and truecolor are tcell's job.

Reject as first choice because:

- Immediate widget mutation is a worse match for coalesced full Snapshots than `Msg`.
- Versioning is `@master` plus a pre-v1 tag that lags tcell.
- No first-party golden/model test story comparable to Bubble Tea model tests + teatest.
- k9s — the headline tview app — does **not** use upstream tview; it requires `github.com/derailed/tview` and `github.com/derailed/tcell/v2` ([k9s `go.mod`](https://raw.githubusercontent.com/derailed/k9s/master/go.mod)).
- Adopting tview would add tcell (and later a v2→v3 decision) beside Charm's ultraviolet if any Charm package leaked in. Keep one renderer.

## Rejected / wrong-fit

- **Bubble Tea v1 (`github.com/charmbracelet/bubbletea` v1.3.10):** frozen relative to v2. Imperative `WithAltScreen()` / `EnterAltScreen` are removed in v2. Starting there forces a migration the greenfield TUI can skip. v1 also does **not** use tcell (its graph is lipgloss/termenv/x/term).
- **Wish:** SSH-hosted TUIs, not a local Manager client.
- **Huh / Survey / promptui as the TUI:** prompt/form libraries. Survey is **unmaintained**; the README tells readers to use Bubble Tea ([Survey README](https://raw.githubusercontent.com/AlecAivazis/survey/master/README.md)). promptui last tag **v0.9.0** (2021-10-30), BSD-3-Clause, depends on an old readline ([pkg.go.dev](https://pkg.go.dev/github.com/manifoldco/promptui)). Fine for a one-shot host picker; this product already uses Cobra flags and does not need a second prompt stack for the live view.
- **gocui (jroimartin, awesome-gocui, jesseduffield):** view/keybinding immediate mode. Original `jroimartin/gocui` last tag v0.5.0 (2021-08-14), BSD-3-Clause ([LICENSE](https://raw.githubusercontent.com/jroimartin/gocui/master/LICENSE)). `awesome-gocui/gocui` last tag v1.1.0 (2022-01-13). Jesse's fork is no longer a standalone dependency for lazygit: commit [95c237f](https://github.com/jesseduffield/lazygit/commit/95c237fdbbf249b9d1057039c7310f3c02ba8839) (2026-04-30) copied gocui into `pkg/gocui` because the fork had become lazygit-specific. Current lazygit `go.mod` requires `tcell/v3` and **not** an external gocui module ([lazygit `go.mod`](https://raw.githubusercontent.com/jesseduffield/lazygit/master/go.mod)). Fork fragmentation is a reason to stay away.
- **gizak/termui:** dashboard widgets on termbox. Last tag **v3.1.0** (2019-07-15), MIT ([LICENSE](https://raw.githubusercontent.com/gizak/termui/master/LICENSE)). Wrong shape (graphs/gauges), unmaintained relative to 2026.
- **mum4k/termdash:** terminal **dashboard**. Last tag **v0.20.0** (2024-03-10), Apache-2.0, public API still pre-1.0 ([README](https://raw.githubusercontent.com/mum4k/termdash/master/README.md)). Prefer tcell over termbox in its own termbox wrapper docs. Wrong product (metrics dashboards vs a Watch list).
- **nsf/termbox-go:** the author's README states the library is “somewhat not maintained” and points new work at tcell ([README](https://raw.githubusercontent.com/nsf/termbox-go/master/README.md)). Last tag v1.1.1 (2021-04-21). MIT.
- **marcusolsson/tui-go:** archived; README recommends tview and says not to use it beyond experiments ([README](https://raw.githubusercontent.com/marcusolsson/tui-go/master/README.md)).
- **c-bata/go-prompt / chzyer/readline:** line editors with completion. go-prompt last tag v0.2.6 (2021-03-03), MIT ([pkg.go.dev](https://pkg.go.dev/github.com/c-bata/go-prompt)); readline last tag v1.5.1 (2022-07-15), MIT ([pkg.go.dev](https://pkg.go.dev/github.com/chzyer/readline)). Irrelevant to Snapshot Watch. Do not add them for host picking; Cobra already owns that CLI surface.

## Production evidence (verified `go.mod` / README)

| App | Stack (from the app's own files) |
|---|---|
| **Glow** (Charm markdown TUI) | `charm.land/bubbletea/v2 v2.0.8`, `charm.land/bubbles/v2`, `charm.land/lipgloss/v2`, **Cobra** ([`go.mod`](https://raw.githubusercontent.com/charmbracelet/glow/master/go.mod)). |
| **Crush** (Charm) | Bubble Tea v2.0.8 + Bubbles v2.1.1 + Lip Gloss v2 ([`go.mod`](https://raw.githubusercontent.com/charmbracelet/crush/master/go.mod)). Charm's v2 blog states v2 branches powered Crush in production ([blog](https://charm.land/blog/v2.md)). |
| **dry** (Docker TUI) | Migrated off termbox: current `go.mod` requires Bubble Tea v2 / Bubbles / Lip Gloss ([`go.mod`](https://raw.githubusercontent.com/moncho/dry/master/go.mod)). Historical termbox README still lists dry; believe the module file. |
| **lazygit** | Vendored gocui + **tcell v3.4.1**, not Bubble Tea ([`go.mod`](https://raw.githubusercontent.com/jesseduffield/lazygit/master/go.mod), [vendoring commit](https://github.com/jesseduffield/lazygit/commit/95c237fdbbf249b9d1057039c7310f3c02ba8839)). |
| **k9s** | **Forked** tview/tcell (`github.com/derailed/tview`, `github.com/derailed/tcell/v2`) plus Cobra ([`go.mod`](https://raw.githubusercontent.com/derailed/k9s/master/go.mod)). |
| **GitHub CLI (`gh`)** | Mixed: Charm v2 (`bubbletea`/`huh`/`lipgloss`) **and** `rivo/tview` + `tcell/v2` **and** still `AlecAivazis/survey/v2` ([`go.mod`](https://raw.githubusercontent.com/cli/cli/trunk/go.mod)). Evidence that Charm and tview can live in a large Cobra binary; not a model to copy. tview's README lists gh as a user ([tview README](https://raw.githubusercontent.com/rivo/tview/master/README.md)). |

Bubble Tea's own README lists Glow, Huh, and industry tools; those “in the wild” names were not re-verified beyond Glow/Crush/dry/`gh` above.

## Minimal composition sketch (not an implementation)

Do not start this until the CLI slice is done. When it starts:

```
ssh-forward tui
  Cobra RunE (TTY required; refuse --json)
    sess := app.Connect(ctx, opts)     // same singleton path as status/watch
    defer sess.Close()
    stream := sess.Manager.Watch(ctx)  // JSON-RPC Watch, coalesced Snapshots
    p := tea.NewProgram(newModel(sess), tea.WithContext(ctx))
    go func() {
      for {
        snap, err := stream.Recv()     // product API; full Snapshot
        if err != nil { p.Send(errMsg{err}); return }
        p.Send(snapshotMsg{snap})      // replace view-model; keep cursor if possible
      }
    }()
    _, err := p.Run()                  // alt-screen View; restore on exit
```

Remember/forget keys call the same app helpers as `add`/`remove`. The model stores the last Snapshot plus UI chrome. Tests drive `Update` with `snapshotMsg` and `KeyPressMsg` against a fake Manager; no ultraviolet/tcell in unit tests.

## Risks

1. **Charm v2 renderer (ultraviolet)** is a new cell renderer, pulled as a pseudoversion from Bubble Tea v2.0.8. Pin the trio and run alt-screen restore tests on the four target terminals.
2. **`charm.land` vanity paths** vs old `github.com/charmbracelet/...` v1 paths. Do not mix v1 widgets with a v2 program.
3. **teatest/v2** is still experimental and was not graph-aligned with v2.0.8 on this date. Prefer model unit tests.
4. **Bubbles extra deps** (harmonica, clipboard, fuzzy). Accept them for list/viewport; do not import huh “because it is Charm.”
5. **Do not default-interactive** until `missingCommand` and `--json` policy are explicit. A subcommand keeps scripts safe.

## Sources checked

Fetched or queried on 2026-08-18 (live). GitHub API releases returned 403; versions use the Go module proxy and pkg.go.dev. Some `raw.githubusercontent.com` requests returned 429 mid-session; successful fetches are listed.

- https://raw.githubusercontent.com/charmbracelet/bubbletea/master/README.md
- https://raw.githubusercontent.com/charmbracelet/bubbletea/master/go.mod
- https://raw.githubusercontent.com/charmbracelet/bubbletea/master/LICENSE
- https://raw.githubusercontent.com/charmbracelet/bubbletea/master/UPGRADE_GUIDE_V2.md
- https://raw.githubusercontent.com/charmbracelet/bubbletea/v2.0.8/options.go
- https://raw.githubusercontent.com/charmbracelet/bubbletea/main/examples/send-msg/main.go
- https://github.com/charmbracelet/bubbletea/blob/main/tea.go
- https://github.com/charmbracelet/bubbletea/releases/tag/v2.0.0
- https://charm.land/blog/v2.md
- https://charm.land/blog/teatest/
- https://pkg.go.dev/charm.land/bubbletea/v2
- https://pkg.go.dev/github.com/charmbracelet/bubbletea
- https://proxy.golang.org/charm.land/bubbletea/v2/@v/v2.0.8.info
- https://proxy.golang.org/github.com/charmbracelet/bubbletea/@v/v1.3.10.info
- https://raw.githubusercontent.com/charmbracelet/bubbles/master/README.md
- https://pkg.go.dev/charm.land/bubbles/v2
- https://proxy.golang.org/charm.land/bubbles/v2/@v/v2.1.1.info
- https://raw.githubusercontent.com/charmbracelet/lipgloss/master/README.md
- https://raw.githubusercontent.com/charmbracelet/lipgloss/master/go.mod
- https://raw.githubusercontent.com/charmbracelet/lipgloss/v2.0.6/LICENSE
- https://pkg.go.dev/charm.land/lipgloss/v2
- https://proxy.golang.org/charm.land/lipgloss/v2/@v/v2.0.6.info
- https://raw.githubusercontent.com/charmbracelet/huh/master/README.md
- https://raw.githubusercontent.com/charmbracelet/huh/v2.0.3/LICENSE
- https://pkg.go.dev/charm.land/huh/v2
- https://pkg.go.dev/github.com/charmbracelet/harmonica
- https://raw.githubusercontent.com/charmbracelet/harmonica/v0.2.0/LICENSE
- https://pkg.go.dev/github.com/charmbracelet/wish
- https://pkg.go.dev/charm.land/wish/v2
- https://pkg.go.dev/github.com/charmbracelet/x/exp/teatest
- https://pkg.go.dev/github.com/charmbracelet/x/exp/teatest/v2
- https://raw.githubusercontent.com/charmbracelet/x/main/LICENSE
- https://raw.githubusercontent.com/charmbracelet/colorprofile/v0.4.3/README.md
- https://raw.githubusercontent.com/charmbracelet/ultraviolet/master/LICENSE
- https://raw.githubusercontent.com/rivo/tview/master/README.md
- https://raw.githubusercontent.com/rivo/tview/v0.42.0/LICENSE.txt
- https://raw.githubusercontent.com/rivo/tview/v0.42.0/application.go
- https://pkg.go.dev/github.com/rivo/tview
- https://pkg.go.dev/github.com/gdamore/tcell/v2
- https://pkg.go.dev/github.com/gdamore/tcell/v3
- https://raw.githubusercontent.com/gdamore/tcell/v2.13.10/LICENSE
- https://raw.githubusercontent.com/gdamore/tcell/v3.4.1/LICENSE
- https://raw.githubusercontent.com/gdamore/tcell/master/go.mod
- https://raw.githubusercontent.com/charmbracelet/glow/master/go.mod
- https://raw.githubusercontent.com/charmbracelet/crush/master/go.mod
- https://raw.githubusercontent.com/moncho/dry/master/go.mod
- https://raw.githubusercontent.com/jesseduffield/lazygit/master/go.mod
- https://github.com/jesseduffield/lazygit/commit/95c237fdbbf249b9d1057039c7310f3c02ba8839
- https://raw.githubusercontent.com/derailed/k9s/master/go.mod
- https://raw.githubusercontent.com/cli/cli/trunk/go.mod
- https://raw.githubusercontent.com/nsf/termbox-go/master/README.md
- https://raw.githubusercontent.com/nsf/termbox-go/master/LICENSE
- https://raw.githubusercontent.com/gizak/termui/master/LICENSE
- https://raw.githubusercontent.com/mum4k/termdash/master/README.md
- https://raw.githubusercontent.com/mum4k/termdash/master/LICENSE
- https://raw.githubusercontent.com/marcusolsson/tui-go/master/README.md
- https://raw.githubusercontent.com/AlecAivazis/survey/master/README.md
- https://raw.githubusercontent.com/jroimartin/gocui/master/LICENSE
- https://pkg.go.dev/github.com/jroimartin/gocui
- https://pkg.go.dev/github.com/awesome-gocui/gocui
- https://pkg.go.dev/github.com/jesseduffield/gocui
- https://pkg.go.dev/github.com/manifoldco/promptui
- https://pkg.go.dev/github.com/c-bata/go-prompt
- https://pkg.go.dev/github.com/chzyer/readline
- https://proxy.golang.org/github.com/gizak/termui/v3/@v/v3.1.0.info
- https://proxy.golang.org/github.com/mum4k/termdash/@v/v0.20.0.info
- https://proxy.golang.org/github.com/nsf/termbox-go/@v/v1.1.1.info
- https://pkg.go.dev/github.com/mum4k/termdash/terminal/termbox (termbox wrapper: prefer tcell; termbox-go unmaintained)
