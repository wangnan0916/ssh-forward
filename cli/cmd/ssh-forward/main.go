// Command ssh-forward is the domain-oriented CLI (slices 6–7,
// implementation-sequence.md): it auto-spawns a per-user manager, exposes
// the product domain's command surface, emits wire-shaped --json output,
// and can serve a loopback WebUI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/wangnan0916/ssh-forward/cli/internal/cli"
)

// buildVersion is the product version, bumped with each release tag; the
// formula's test and the brew audit both key off it.
const buildVersion = "0.1.0-alpha.1"

// versionString reports the product version plus the embedded build
// revision (Go stamps vcs.revision into the binary), so --version tells a
// HEAD install apart by its actual commit.
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return buildVersion
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && len(setting.Value) >= 7 {
			return buildVersion + "+" + setting.Value[:7]
		}
	}
	return buildVersion
}

func main() {
	// The context is cancellable so watch (and other long-running
	// surfaces) end on Ctrl-C; the shell convention reports an interrupt
	// as 128+SIGINT instead of a silent success.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	if code == 0 && ctx.Err() != nil {
		code = 130
	}
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	surface := &cli.App{Version: versionString()}
	surface.Options.Stdin = stdin
	surface.Options.Stdout = stdout
	surface.Options.Stderr = stderr
	if err := surface.Run(ctx, args); err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		if errors.Is(err, cli.ErrUsage) {
			return 2
		}
		return 1
	}
	return 0
}
