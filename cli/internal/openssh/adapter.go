package openssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

var ErrInvalidAlias = errors.New("invalid Development Host alias")

const (
	maxStderrTailBytes     = 64 << 10
	maxTemporaryPortOffset = 20
	forwardProbeInterval   = 25 * time.Millisecond
	forwardDialTimeout     = 50 * time.Millisecond
	maximumTCPPort         = 1<<16 - 1
)

type Options struct {
	Executable       string
	ConfigFile       string
	ControlDirectory string
	ReadyTimeout     time.Duration
	WaitDelay        time.Duration
}

// Adapter observes remote listeners and reconciles local forwards through one
// product-private OpenSSH master per host. OpenSSH owns the forwarding data
// plane.
type Adapter struct {
	executable       string
	configFile       string
	controlDirectory string
	readyTimeout     time.Duration
	waitDelay        time.Duration
	environment      []string

	mu      sync.Mutex
	closed  bool
	masters map[core.HostAlias]*sshMaster
}

func New(options Options) (*Adapter, error) {
	if !filepath.IsAbs(options.Executable) {
		return nil, errors.New("OpenSSH executable path is not absolute")
	}
	if options.ConfigFile != "" && !filepath.IsAbs(options.ConfigFile) {
		return nil, errors.New("OpenSSH config path is not absolute")
	}
	if !filepath.IsAbs(options.ControlDirectory) {
		return nil, errors.New("OpenSSH control directory path is not absolute")
	}
	if err := os.MkdirAll(options.ControlDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create OpenSSH control directory: %w", err)
	}
	info, err := os.Stat(options.ControlDirectory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("OpenSSH control directory must not be writable by other users")
	}
	if options.ReadyTimeout <= 0 {
		options.ReadyTimeout = 10 * time.Second
	}
	if options.WaitDelay <= 0 {
		options.WaitDelay = 2 * time.Second
	}
	return &Adapter{
		executable:       options.Executable,
		configFile:       options.ConfigFile,
		controlDirectory: options.ControlDirectory,
		readyTimeout:     options.ReadyTimeout,
		waitDelay:        options.WaitDelay,
		environment:      approvedEnvironment(),
		masters:          make(map[core.HostAlias]*sshMaster),
	}, nil
}

func (a *Adapter) Observe(ctx context.Context, host core.HostAlias, emit func([]core.Listener)) error {
	alias := string(host)
	if !validAlias(alias) {
		return backendError("invalid_alias")
	}
	master, err := a.ensureMaster(ctx, host)
	if err != nil {
		return err
	}
	arguments := append(a.configArguments(),
		"-S", controlSocketTemplate, "-T", "-o", "ControlMaster=no",
		alias, "sh", "-s",
	)
	command := a.command(arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	command.Stdin = strings.NewReader(scannerScript)
	command.Stderr = stderr
	configureProcess(command)
	if err := command.Start(); err != nil {
		return err
	}
	stopContextWatch := context.AfterFunc(ctx, func() { _ = terminateProcess(command) })
	defer stopContextWatch()
	stopMasterWatch := make(chan struct{})
	defer close(stopMasterWatch)
	go func() {
		select {
		case <-master.done:
			_ = terminateProcess(command)
		case <-stopMasterWatch:
		}
	}()
	scanErr := scanListenerFrames(stdout, emit)
	if scanErr != nil {
		_ = terminateProcess(command)
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	select {
	case <-master.done:
		return master.failure()
	default:
	}
	if scanErr != nil {
		return backendError("discovery_invalid")
	}
	return classifyError(waitErr, stderr.String())
}

func (a *Adapter) Forward(
	ctx context.Context,
	host core.HostAlias,
	target core.ForwardTarget,
	ready func(localPort uint16),
) error {
	alias := string(host)
	if !validAlias(alias) {
		return backendError("invalid_alias")
	}
	master, err := a.ensureMaster(ctx, host)
	if err != nil {
		return err
	}
	localPort, forward, err := a.startForward(ctx, host, target)
	if err != nil {
		return err
	}
	defer a.cancelForward(host, forward)
	if err := a.waitForForward(ctx, master, localPort); err != nil {
		return err
	}
	ready(localPort)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-master.done:
		return master.failure()
	}
}

func (a *Adapter) startForward(
	ctx context.Context,
	host core.HostAlias,
	target core.ForwardTarget,
) (uint16, string, error) {
	for offset := 0; ; offset++ {
		localPort := target.LocalPort + uint16(offset)
		forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, target.RemotePort)
		err := a.runControl(ctx, host, "forward", forward)
		if err == nil {
			return localPort, forward, nil
		}
		// The mux client reports only a generic failure for local bind errors;
		// OpenSSH writes the useful detail to the long-lived master process.
		if core.ErrorDiagnostic(err) == "transport_unavailable" {
			if checkErr := a.runControl(ctx, host, "check", ""); checkErr == nil {
				err = backendError("local_port_conflict")
			}
		}
		a.cancelForward(host, forward)
		if !target.AllowFallback ||
			core.ErrorDiagnostic(err) != "local_port_conflict" ||
			offset == maxTemporaryPortOffset || localPort == maximumTCPPort {
			return 0, "", err
		}
	}
}

func (a *Adapter) waitForForward(ctx context.Context, master *sshMaster, port uint16) error {
	deadline := time.NewTimer(a.readyTimeout)
	probe := time.NewTicker(forwardProbeInterval)
	defer deadline.Stop()
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-master.done:
			return master.failure()
		case <-deadline.C:
			return backendError("forward_start_timeout")
		case <-probe.C:
			connection, err := net.DialTimeout(
				"tcp4", fmt.Sprintf("127.0.0.1:%d", port), forwardDialTimeout,
			)
			if err != nil {
				continue
			}
			_ = connection.Close()
			return nil
		}
	}
}

func (a *Adapter) command(arguments ...string) *exec.Cmd {
	command := exec.Command(a.executable, arguments...)
	a.configureCommand(command)
	return command
}

func (a *Adapter) commandContext(ctx context.Context, arguments ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, a.executable, arguments...)
	a.configureCommand(command)
	return command
}

func (a *Adapter) configureCommand(command *exec.Cmd) {
	command.Dir = a.controlDirectory
	command.Env = a.environment
	command.WaitDelay = a.waitDelay
}

func (a *Adapter) validateAlias(ctx context.Context, alias string) error {
	if !validAlias(alias) {
		return ErrInvalidAlias
	}
	arguments := append(a.configArguments(), "-G", alias)
	command := a.commandContext(ctx, arguments...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	return command.Run()
}

func (a *Adapter) configArguments() []string {
	if a.configFile == "" {
		return nil
	}
	return []string{"-F", a.configFile}
}

func backendError(diagnostic string) error {
	return &core.BackendError{Diagnostic: diagnostic}
}

func classifyError(err error, stderr string) error {
	if err == nil {
		return backendError("transport_unavailable")
	}
	message := strings.ToLower(stderr)
	switch {
	case strings.Contains(message, "permission denied"),
		strings.Contains(message, "too many authentication failures"),
		strings.Contains(message, "no supported authentication methods"):
		return backendError("authentication_failed")
	case strings.Contains(message, "host key verification failed"),
		strings.Contains(message, "remote host identification has changed"):
		return backendError("host_key_failed")
	case strings.Contains(message, "address already in use"),
		strings.Contains(message, "cannot listen to port"):
		return backendError("local_port_conflict")
	default:
		return backendError("transport_unavailable")
	}
}

func approvedEnvironment() []string {
	allowed := map[string]bool{
		"DISPLAY": true, "HOME": true, "LANG": true, "LOGNAME": true,
		"PATH": true, "SHELL": true, "SSH_AUTH_SOCK": true, "TERM": true,
		"TMPDIR": true, "USER": true,
	}
	var environment []string
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && (allowed[name] || strings.HasPrefix(name, "LC_")) {
			environment = append(environment, entry)
		}
	}
	slices.Sort(environment)
	return environment
}

func validAlias(alias string) bool {
	if len(alias) == 0 || len(alias) > core.MaxHostAliasLength || alias[0] == '-' || !utf8.ValidString(alias) {
		return false
	}
	for _, character := range alias {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

type boundedBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(data) >= b.limit {
		b.data = append(b.data[:0], data[len(data)-b.limit:]...)
		return len(data), nil
	}
	overflow := max(len(b.data)+len(data)-b.limit, 0)
	if overflow > 0 {
		b.data = append(b.data[:0], b.data[overflow:]...)
	}
	b.data = append(b.data, data...)
	return len(data), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
