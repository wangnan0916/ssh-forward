package openssh

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ssh-forward/cli/internal/core"
	"ssh-forward/cli/internal/proxy"
)

var ErrInvalidAlias = errors.New("invalid Development Host alias")

type Options struct {
	Executable   string
	ConfigFile   string
	ReadyTimeout time.Duration
	WaitDelay    time.Duration
}

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
	readyTimeout := options.ReadyTimeout
	if readyTimeout <= 0 {
		readyTimeout = 10 * time.Second
	}
	waitDelay := options.WaitDelay
	if waitDelay <= 0 {
		waitDelay = 2 * time.Second
	}
	return &Adapter{
		executable:   options.Executable,
		configFile:   options.ConfigFile,
		readyTimeout: readyTimeout,
		waitDelay:    waitDelay,
	}, nil
}

func (a *Adapter) Connect(ctx context.Context, host core.HostAlias) (core.HostSession, error) {
	alias := string(host)
	if err := a.ValidateAlias(ctx, alias); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &core.SessionError{
			Disposition: core.SessionSuspend,
			Reason:      core.SessionReasonInvalidAlias,
			Diagnostic:  "alias_validation_failed",
		}
	}
	session, err := a.Start(ctx, alias)
	if err == nil {
		return session, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var sessionError *core.SessionError
	if errors.As(err, &sessionError) {
		return nil, sessionError
	}
	return nil, retryTransportError()
}

func (a *Adapter) ValidateAlias(ctx context.Context, alias string) error {
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

func (a *Adapter) Start(ctx context.Context, alias string) (*Session, error) {
	if !validAlias(alias) {
		return nil, ErrInvalidAlias
	}
	socksAddress, err := reserveSOCKSAddress()
	if err != nil {
		return nil, err
	}
	arguments := append(a.configArguments(),
		"-T",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ExitOnForwardFailure=yes",
		"-D", socksAddress.String(),
		alias,
		"sh", "-s",
	)
	command := exec.Command(a.executable, arguments...)
	stderr := &boundedBuffer{limit: 64 << 10}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stdin = strings.NewReader(scannerScript)
	command.Stderr = stderr
	command.Env = approvedEnvironment()
	command.WaitDelay = a.waitDelay
	configureProcess(command)
	if err := command.Start(); err != nil {
		return nil, err
	}
	session := &Session{
		command:     command,
		dialer:      proxy.NewSOCKS5Dialer(socksAddress),
		done:        make(chan struct{}),
		stderr:      stderr,
		facts:       newSessionFactQueue(),
		scannerDone: make(chan struct{}),
	}
	go session.readScanner(stdout)
	go session.wait()
	if err := session.waitUntilReady(ctx, socksAddress, a.readyTimeout); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Close(cleanupCtx)
		return nil, err
	}
	return session, nil
}

func reserveSOCKSAddress() (netip.AddrPort, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return netip.AddrPort{}, err
	}
	address := listener.Addr().(*net.TCPAddr).AddrPort()
	if err := listener.Close(); err != nil {
		return netip.AddrPort{}, err
	}
	return address, nil
}

func (a *Adapter) configArguments() []string {
	if a.configFile == "" {
		return nil
	}
	return []string{"-F", a.configFile}
}

func approvedEnvironment() []string {
	allowed := map[string]bool{
		"DISPLAY":       true,
		"HOME":          true,
		"LANG":          true,
		"LC_ALL":        true,
		"LC_CTYPE":      true,
		"LOGNAME":       true,
		"PATH":          true,
		"SHELL":         true,
		"SSH_AUTH_SOCK": true,
		"TERM":          true,
		"TMPDIR":        true,
		"USER":          true,
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
	if len(alias) == 0 || len(alias) > 255 || alias[0] == '-' || !utf8.ValidString(alias) {
		return false
	}
	for _, character := range alias {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}
