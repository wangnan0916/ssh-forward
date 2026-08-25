package openssh

import (
	"context"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

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
