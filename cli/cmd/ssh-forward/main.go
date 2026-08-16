// Command ssh-forward is the domain-oriented CLI (slice 6,
// implementation-sequence.md): it runs the headless Manager in-process,
// exposes the product domain's command surface, and emits wire-shaped
// --json output for scripts and desktop clients.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"ssh-forward/cli/internal/app"
	"ssh-forward/cli/internal/cli"
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/openssh"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ssh-forward", flag.ContinueOnError)
	flags.SetOutput(stderr)
	host := flags.String("host", "", "Development Host SSH alias (required)")
	policies := flags.String("policies", defaultPoliciesPath(), "path to policies.jsonc")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	rest := flags.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: ssh-forward [--host ALIAS] [--policies PATH] COMMAND ...")
		fmt.Fprintln(stderr, "commands: status, forward add|remove, listener approve|suppress, policy list")
		return 2
	}
	if *host == "" {
		fmt.Fprintln(stderr, "ssh-forward: --host is required")
		return 2
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
	manager := app.NewManager(core.HostAlias(*host), adapter, app.FilePolicySource(*policies))
	defer func() { _ = manager.Close(context.Background()) }()

	app := &cli.App{
		Manager:      manager,
		Host:         core.HostAlias(*host),
		PoliciesPath: *policies,
		Stdout:       stdout,
		Stderr:       stderr,
	}
	if err := app.Run(context.Background(), rest); err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 1
	}
	return 0
}

// defaultPoliciesPath resolves the product configuration directory per
// cli-and-state.md: SSH_FORWARD_CONFIG_DIR overrides it, then the platform
// application-support locations.
func defaultPoliciesPath() string {
	if override := os.Getenv("SSH_FORWARD_CONFIG_DIR"); override != "" {
		return filepath.Join(override, "policies.jsonc")
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
	return filepath.Join(base, "policies.jsonc")
}
