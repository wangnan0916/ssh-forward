// Command ssh-forward is the domain-oriented CLI (slice 6,
// implementation-sequence.md): it runs the headless Manager in-process,
// exposes the product domain's command surface, and emits wire-shaped
// --json output for scripts and desktop clients.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"ssh-forward/cli/internal/app"
	"ssh-forward/cli/internal/cli"
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

func main() {
	// The context is cancellable so watch (and other long-running
	// surfaces) end on Ctrl-C; the shell convention reports an interrupt
	// as 128+SIGINT instead of a silent success.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if code == 0 && ctx.Err() != nil {
		code = 130
	}
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ssh-forward", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "Development Host SSH alias (defaults to config.jsonc's default_host)")
	policies := flags.String("policies", defaultPoliciesPath(), "path to policies.jsonc")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	rest := flags.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: ssh-forward [--host ALIAS] [--policies PATH] COMMAND ...")
		fmt.Fprintln(stderr, "commands: status, watch, forward add|remove, listener approve|suppress, policy list")
		return 2
	}
	resolvedHost := *host
	if resolvedHost == "" {
		defaulted, err := defaultHost()
		if err != nil {
			fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
			return 2
		}
		resolvedHost = defaulted
	}

	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Fprintf(stderr, "ssh-forward: cannot find the OpenSSH client: %v\n", err)
		return 1
	}
	adapter, err := openssh.New(openssh.Options{Executable: sshPath})
	if err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 1
	}
	policyReader := app.NewFilePolicyReader(*policies)
	manager := app.NewManager(core.HostAlias(resolvedHost), adapter, policyReader.Source())
	defer func() { _ = manager.Close(context.Background()) }()

	app := &cli.App{
		Manager:      manager,
		Host:         core.HostAlias(resolvedHost),
		PoliciesPath: *policies,
		PolicyReader: policyReader,
		Stdout:       stdout,
		Stderr:       stderr,
	}
	if err := app.Run(ctx, rest); err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 1
	}
	return 0
}

// configDir resolves the product configuration directory per
// cli-and-state.md: SSH_FORWARD_CONFIG_DIR overrides it, then the platform
// application-support locations. Both config.jsonc and policies.jsonc live
// here.
func configDir() string {
	if override := os.Getenv("SSH_FORWARD_CONFIG_DIR"); override != "" {
		return override
	}
	var base string
	switch runtime.GOOS {
	case "darwin":
		base = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "ssh-forward")
	case "linux":
		base = os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(os.Getenv("HOME"), ".config")
		}
		base = filepath.Join(base, "ssh-forward")
	case "windows":
		base = filepath.Join(os.Getenv("APPDATA"), "ssh-forward")
	default:
		base = "."
	}
	return base
}

func defaultPoliciesPath() string { return filepath.Join(configDir(), "policies.jsonc") }

func defaultConfigPath() string { return filepath.Join(configDir(), "config.jsonc") }

// defaultHost resolves the Development Host alias: --host wins; otherwise
// config.jsonc's default_host (the Persistent intent contract). A missing
// config or a missing default_host is a usage error like a missing flag; a
// corrupt config is diagnosed precisely.
func defaultHost() (string, error) {
	config, err := app.LoadConfig(defaultConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("no --host given and no config.jsonc default host")
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", defaultConfigPath(), err)
	}
	if config.DefaultHost == "" {
		return "", errors.New("no --host given and config.jsonc has no default_host")
	}
	return config.DefaultHost, nil
}
