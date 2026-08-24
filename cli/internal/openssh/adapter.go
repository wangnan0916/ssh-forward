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

const maxStderrTailBytes = 64 << 10

type Options struct {
	Executable   string
	ConfigFile   string
	ReadyTimeout time.Duration
	WaitDelay    time.Duration
}

// Adapter observes remote listeners and keeps one OpenSSH local forward
// alive. OpenSSH owns the forwarding data plane.
type Adapter struct {
	executable   string
	configFile   string
	readyTimeout time.Duration
	waitDelay    time.Duration
}

func New(options Options) (*Adapter, error) {
	if !filepath.IsAbs(options.Executable) {
		return nil, errors.New("OpenSSH executable path is not absolute")
	}
	if options.ConfigFile != "" && !filepath.IsAbs(options.ConfigFile) {
		return nil, errors.New("OpenSSH config path is not absolute")
	}
	if options.ReadyTimeout <= 0 {
		options.ReadyTimeout = 10 * time.Second
	}
	if options.WaitDelay <= 0 {
		options.WaitDelay = 2 * time.Second
	}
	return &Adapter{
		executable: options.Executable, configFile: options.ConfigFile,
		readyTimeout: options.ReadyTimeout, waitDelay: options.WaitDelay,
	}, nil
}

func (a *Adapter) Observe(ctx context.Context, host core.HostAlias, emit func([]core.Listener)) error {
	alias := string(host)
	if err := a.validateAlias(ctx, alias); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return backendError("invalid_alias")
	}
	arguments := append(a.configArguments(),
		"-T", "-o", "ControlMaster=no", "-o", "ControlPath=none",
		alias, "sh", "-s",
	)
	command := exec.Command(a.executable, arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	command.Stdin = strings.NewReader(scannerScript)
	command.Stderr = stderr
	command.Env = approvedEnvironment()
	command.WaitDelay = a.waitDelay
	configureProcess(command)
	if err := command.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = terminateProcess(command) })
	scanErr := scanListenerFrames(stdout, emit)
	if scanErr != nil {
		_ = terminateProcess(command)
	}
	waitErr := command.Wait()
	stop()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanErr != nil {
		return backendError("discovery_invalid")
	}
	return classifyError(waitErr, stderr.String())
}

func (a *Adapter) Forward(ctx context.Context, host core.HostAlias, port uint16, ready func()) error {
	alias := string(host)
	if !validAlias(alias) {
		return backendError("invalid_alias")
	}
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port)
	arguments := append(a.configArguments(),
		"-N", "-T", "-o", "ControlMaster=no", "-o", "ControlPath=none",
		"-o", "ExitOnForwardFailure=yes", "-L", forward, alias,
	)
	command := exec.Command(a.executable, arguments...)
	stderr := &boundedBuffer{limit: maxStderrTailBytes}
	command.Stderr = stderr
	command.Stdout = io.Discard
	command.Env = approvedEnvironment()
	command.WaitDelay = a.waitDelay
	configureProcess(command)
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	deadline := time.NewTimer(a.readyTimeout)
	probe := time.NewTicker(25 * time.Millisecond)
	defer deadline.Stop()
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			stopProcess(command, done, a.waitDelay)
			return ctx.Err()
		case err := <-done:
			return classifyError(err, stderr.String())
		case <-deadline.C:
			stopProcess(command, done, a.waitDelay)
			return backendError("forward_start_timeout")
		case <-probe.C:
			connection, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
			if err != nil {
				continue
			}
			_ = connection.Close()
			ready()
			select {
			case <-ctx.Done():
				stopProcess(command, done, a.waitDelay)
				return ctx.Err()
			case err := <-done:
				return classifyError(err, stderr.String())
			}
		}
	}
}

func stopProcess(command *exec.Cmd, done <-chan error, delay time.Duration) {
	_ = terminateProcess(command)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = killProcess(command)
		<-done
	}
}

func (a *Adapter) validateAlias(ctx context.Context, alias string) error {
	if !validAlias(alias) {
		return ErrInvalidAlias
	}
	arguments := append(a.configArguments(), "-G", alias)
	command := exec.CommandContext(ctx, a.executable, arguments...)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.Env = approvedEnvironment()
	command.WaitDelay = a.waitDelay
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
