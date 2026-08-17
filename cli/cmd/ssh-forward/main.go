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
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"ssh-forward/cli/internal/app"
	"ssh-forward/cli/internal/cli"
	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/ipc"
	"ssh-forward/cli/internal/openssh"
)

// buildVersion is the product version, bumped with each release tag; the
// formula's test and the brew audit both key off it.
const buildVersion = "0.1.0"

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
	sshConfig := flags.String("ssh-config", "", "SSH client config file (default: the user's ~/.ssh/config)")
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "ssh-forward %s\n", buildVersion)
		return 0
	}
	rest := flags.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "usage: ssh-forward [--host ALIAS] [--policies PATH] [--ssh-config PATH] COMMAND ...")
		fmt.Fprintln(stderr, "commands: manager serve, status, watch, forward add|remove, listener approve|suppress, policy list")
		return 2
	}
	if rest[0] == "manager" {
		return runManager(ctx, rest[1:], *host, *policies, *sshConfig, stdout, stderr)
	}

	// Singleton mode (ADR-0016): when the per-user manager runs, every
	// command becomes a client of its Unix socket.
	if clientManager, err := ipc.Dial(ctx, endpointPath()); err == nil {
		return runAsClient(ctx, clientManager, rest, *host, *policies, stdout, stderr)
	}

	// The host resolves before anything may be spawned: a manager needs
	// one Development Host.
	resolvedHost, err := resolveHost(*host)
	if err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 2
	}

	// Auto-spawn: the first command starts the per-user singleton in the
	// background (its own executable, by absolute path) and then becomes
	// its client. SSH_FORWARD_NO_AUTOSPAWN=1 disables this for scripts
	// and tests.
	if os.Getenv("SSH_FORWARD_NO_AUTOSPAWN") != "1" {
		if err := spawnManager(resolvedHost, *policies, *sshConfig); err != nil {
			fmt.Fprintf(stderr, "ssh-forward: could not start the manager: %v\n", err)
			return 1
		}
		if err := waitForManagerEndpoint(ctx, endpointPath(), 5*time.Second); err != nil {
			fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
			return 1
		}
		if clientManager, err := ipc.Dial(ctx, endpointPath()); err == nil {
			return runAsClient(ctx, clientManager, rest, *host, *policies, stdout, stderr)
		}
	}

	// No singleton: run one Manager for this command's lifetime (the
	// in-process fallback keeps scripts and tests on the old model).
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		fmt.Fprintf(stderr, "ssh-forward: cannot find the OpenSSH client: %v\n", err)
		return 1
	}
	adapter, err := buildAdapter(sshPath, *sshConfig)
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

// runAsClient executes one command against the running singleton. The
// manager owns the Development Host; a conflicting --host is a warning,
// not an error.
func runAsClient(ctx context.Context, clientManager core.Manager, rest []string, hostFlag, policies string, stdout, stderr io.Writer) int {
	defer func() { _ = clientManager.Close(context.Background()) }()
	snapshot, err := clientManager.Snapshot(ctx)
	if err != nil || snapshot.Host == nil {
		fmt.Fprintln(stderr, "ssh-forward: the running manager has no Development Host configured")
		return 1
	}
	if hostFlag != "" && hostFlag != string(snapshot.Host.Alias) {
		fmt.Fprintf(stderr, "ssh-forward: warning: --host %s ignored; the running manager owns %s\n", hostFlag, snapshot.Host.Alias)
	}
	app := &cli.App{
		Manager:      clientManager,
		Host:         snapshot.Host.Alias,
		PoliciesPath: policies,
		Stdout:       stdout,
		Stderr:       stderr,
	}
	if err := app.Run(ctx, rest); err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 1
	}
	return 0
}

// resolveHost names the Development Host: --host wins, then config.jsonc's
// default_host.
func resolveHost(hostFlag string) (string, error) {
	if hostFlag != "" {
		return hostFlag, nil
	}
	return defaultHost()
}

// spawnManager starts the per-user singleton in the background: its own
// executable (never $PATH), detached into its own session, logging to
// manager.log next to the socket. The caller never waits on it; the
// manager outlives this command.
func spawnManager(host, policies, sshConfig string) error {
	// SSH_FORWARD_MANAGER_BINARY overrides the executable (tests spawn a
	// real build); production always starts itself by absolute path.
	executable := os.Getenv("SSH_FORWARD_MANAGER_BINARY")
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(configDir(), "manager.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	args := []string{"--host", host, "--policies", policies, "manager", "serve"}
	if sshConfig != "" {
		args = append(args, "--ssh-config", sshConfig)
	}
	command := exec.Command(executable, args...)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	return nil
}

// waitForManagerEndpoint polls the socket until the spawned singleton
// answers or the deadline passes, pointing failures at the manager log.
func waitForManagerEndpoint(ctx context.Context, path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("manager did not start within %s (see %s)", timeout, filepath.Join(configDir(), "manager.log"))
		}
		time.Sleep(20 * time.Millisecond)
	}
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

// buildAdapter constructs the OpenSSH adapter, requiring an absolute
// config path when one is given (the adapter refuses relative ones).
func buildAdapter(sshPath, sshConfig string) (*openssh.Adapter, error) {
	options := openssh.Options{Executable: sshPath}
	if sshConfig != "" {
		absolute, err := filepath.Abs(sshConfig)
		if err != nil {
			return nil, err
		}
		options.ConfigFile = absolute
	}
	return openssh.New(options)
}

// endpointPath is the per-user manager singleton's Unix socket
// (ADR-0016), next to the configuration files.
func endpointPath() string { return filepath.Join(configDir(), "manager.sock") }

// runManager executes the manager command family: serve keeps the per-user
// singleton alive until interrupted, owning the Manager and answering
// compatible CLI and desktop clients over the socket.
func runManager(ctx context.Context, rest []string, hostFlag, policies, sshConfig string, stdout, stderr io.Writer) int {
	if len(rest) != 1 || rest[0] != "serve" {
		fmt.Fprintln(stderr, "usage: ssh-forward manager serve")
		return 2
	}
	resolvedHost := hostFlag
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
	adapter, err := buildAdapter(sshPath, sshConfig)
	if err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 1
	}
	policyReader := app.NewFilePolicyReader(policies)
	manager := app.NewManager(core.HostAlias(resolvedHost), adapter, policyReader.Source())
	defer func() { _ = manager.Close(context.Background()) }()
	// The pid file lets clients and scripts find the singleton; it is
	// removed on a clean stop.
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 1
	}
	pidFile := filepath.Join(configDir(), "manager.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 1
	}
	defer func() { _ = os.Remove(pidFile) }()
	if err := ipc.Serve(ctx, endpointPath(), manager); err != nil {
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		return 1
	}
	return 0
}

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
