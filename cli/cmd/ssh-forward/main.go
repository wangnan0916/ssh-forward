// Command ssh-forward is the domain-oriented CLI (slice 6,
// implementation-sequence.md): it auto-spawns a per-user manager, exposes
// the product domain's command surface, and emits wire-shaped --json output
// for scripts and desktop clients.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/cli"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
	"github.com/wangnan0916/ssh-forward/cli/internal/ipc"
	"github.com/wangnan0916/ssh-forward/cli/internal/openssh"
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
	surface := &cli.App{
		Stdout:  stdout,
		Stderr:  stderr,
		Version: versionString(),
	}
	var closer func()
	defer func() {
		if closer != nil {
			closer()
		}
	}()
	surface.Bind = func() {
		if surface.PoliciesPath == "" {
			surface.PoliciesPath = defaultPoliciesPath()
		}
		if surface.ConfigPath == "" {
			surface.ConfigPath = defaultConfigPath()
		}
	}
	surface.Assemble = func() error {
		return assembleCLI(ctx, surface, stdin, stdout, stderr, &closer)
	}
	surface.ServeManager = func(serveCtx context.Context) error {
		return serveManager(serveCtx, surface)
	}
	if err := surface.Run(ctx, args); err != nil {
		if errors.Is(err, cli.ErrNeedCommand) {
			return 2
		}
		fmt.Fprintf(stderr, "ssh-forward: %v\n", err)
		if errors.Is(err, cli.ErrUsage) {
			return 2
		}
		return 1
	}
	return 0
}

// assembleCLI wires a Manager for one command: prefer the running
// singleton, otherwise resolve the Development Host and auto-spawn (or
// fall back to an in-process Manager for scripts and tests).
func assembleCLI(ctx context.Context, surface *cli.App, stdin io.Reader, stdout, stderr io.Writer, closer *func()) error {
	setManager := func(manager core.Manager) {
		surface.Manager = manager
		*closer = func() { _ = manager.Close(context.Background()) }
	}

	if clientManager, err := ipc.Dial(ctx, endpointPath()); err == nil {
		return attachClient(ctx, surface, clientManager, stderr, setManager)
	}

	resolvedHost, err := resolveHost(surface.HostFlag, surface.SSHConfigPath, isTerminal(stdin), stdin, stdout)
	if err != nil {
		return cli.UsageError(err)
	}

	// Auto-spawn: the first command starts the per-user singleton in the
	// background (its own executable, by absolute path) and then becomes
	// its client. SSH_FORWARD_NO_AUTOSPAWN=1 disables this for scripts
	// and tests.
	if os.Getenv("SSH_FORWARD_NO_AUTOSPAWN") != "1" {
		if err := spawnManager(resolvedHost, surface.PoliciesPath, surface.SSHConfigPath); err != nil {
			return fmt.Errorf("could not start the manager: %w", err)
		}
		if err := waitForManagerEndpoint(ctx, endpointPath(), 5*time.Second); err != nil {
			return err
		}
		if clientManager, err := ipc.Dial(ctx, endpointPath()); err == nil {
			return attachClient(ctx, surface, clientManager, stderr, setManager)
		}
	}

	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("cannot find the OpenSSH client: %w", err)
	}
	adapter, err := buildAdapter(sshPath, surface.SSHConfigPath)
	if err != nil {
		return err
	}
	policyReader := app.NewFilePolicyReader(surface.PoliciesPath)
	manager := app.NewManager(core.HostAlias(resolvedHost), adapter, policyReader.Source())
	surface.Host = core.HostAlias(resolvedHost)
	surface.PolicyReader = policyReader
	setManager(manager)
	return nil
}

func attachClient(ctx context.Context, surface *cli.App, clientManager core.Manager, stderr io.Writer, setManager func(core.Manager)) error {
	snapshot, err := clientManager.Snapshot(ctx)
	if err != nil || snapshot.Host == nil {
		_ = clientManager.Close(context.Background())
		return errors.New("the running manager has no Development Host configured")
	}
	if surface.HostFlag != "" && surface.HostFlag != string(snapshot.Host.Alias) {
		fmt.Fprintf(stderr, "ssh-forward: warning: --host %s ignored; the running manager owns %s\n", surface.HostFlag, snapshot.Host.Alias)
	}
	surface.Host = snapshot.Host.Alias
	setManager(clientManager)
	return nil
}

// resolveHost names the Development Host, in order: --host, then
// config.jsonc's default_host, then — when the SSH client configuration
// names exactly one literal Host alias — that host. A corrupt config is
// diagnosed, not bypassed; ambiguous choices are reported with the
// candidates.
func resolveHost(hostFlag, sshConfigPath string, interactive bool, stdin io.Reader, stdout io.Writer) (string, error) {
	if hostFlag != "" {
		return hostFlag, nil
	}
	if host, err := defaultHost(); err == nil {
		return host, nil
	} else if !errors.Is(err, errNoDefaultHost) {
		return "", err
	}
	if sshConfigPath == "" {
		sshConfigPath = defaultSSHConfigPath()
	}
	hosts, err := app.ConfiguredHosts(sshConfigPath)
	if err != nil {
		return "", err
	}
	switch len(hosts) {
	case 0:
		return "", errNoDefaultHost
	case 1:
		return hosts[0], nil
	default:
		if interactive {
			return chooseHost(hosts, stdin, stdout)
		}
		return "", fmt.Errorf("no host selected; configured hosts: %s (pass one with --host, or set one with: ssh-forward default <alias>)", strings.Join(hosts, ", "))
	}
}

// chooseHost prompts on the terminal for one of the configured hosts.
func chooseHost(hosts []string, stdin io.Reader, stdout io.Writer) (string, error) {
	fmt.Fprintln(stdout, "Multiple Development Hosts are configured; pick one:")
	for index, host := range hosts {
		fmt.Fprintf(stdout, "  %d) %s\n", index+1, host)
	}
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Fprint(stdout, "> ")
		var line string
		if _, err := fmt.Fscanln(stdin, &line); err != nil {
			break
		}
		var choice int
		if _, err := fmt.Sscanf(line, "%d", &choice); err == nil && choice >= 1 && choice <= len(hosts) {
			return hosts[choice-1], nil
		}
		fmt.Fprintln(stdout, "invalid choice; pick a number from the list")
	}
	return "", errors.New("no host selected (set one with: ssh-forward default <alias>)")
}

// isTerminal reports whether the reader is an interactive terminal.
func isTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// errNoDefaultHost reports that neither --host nor config.jsonc named a
// Development Host.
var errNoDefaultHost = errors.New("no --host given and no config.jsonc default host")

// defaultSSHConfigPath is the user's SSH client configuration, read for
// host discovery. The adapter itself still lets ssh read it natively.
func defaultSSHConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
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

// serveManager keeps the per-user singleton alive until interrupted,
// owning the Manager and answering compatible CLI and desktop clients
// over the socket.
func serveManager(ctx context.Context, surface *cli.App) error {
	resolvedHost, err := resolveHost(surface.HostFlag, surface.SSHConfigPath, false, nil, surface.Stdout)
	if err != nil {
		return cli.UsageError(err)
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("cannot find the OpenSSH client: %w", err)
	}
	adapter, err := buildAdapter(sshPath, surface.SSHConfigPath)
	if err != nil {
		return err
	}
	policyReader := app.NewFilePolicyReader(surface.PoliciesPath)
	manager := app.NewManager(core.HostAlias(resolvedHost), adapter, policyReader.Source())
	defer func() { _ = manager.Close(context.Background()) }()
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return err
	}
	pidFile := filepath.Join(configDir(), "manager.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		return err
	}
	defer func() { _ = os.Remove(pidFile) }()
	return ipc.Serve(ctx, endpointPath(), manager)
}

// defaultHost resolves the Development Host alias: --host wins; otherwise
// config.jsonc's default_host (the Persistent intent contract). A missing
// config or a missing default_host is a usage error like a missing flag; a
// corrupt config is diagnosed precisely.
func defaultHost() (string, error) {
	config, err := app.LoadConfig(defaultConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", errNoDefaultHost
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", defaultConfigPath(), err)
	}
	if config.DefaultHost == "" {
		return "", errNoDefaultHost
	}
	return config.DefaultHost, nil
}
