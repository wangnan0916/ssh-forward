package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrNoHost reports that neither --host nor config.jsonc named a
// Development Host, and the SSH client configuration did not yield one.
var ErrNoHost = errors.New("No Development Host is set. List aliases: ssh-forward host\nThen pin one: ssh-forward default ALIAS")

// ResolutionError is a Development Host naming failure that the CLI
// reports as usage (exit 2).
type ResolutionError struct {
	err error
}

func (e *ResolutionError) Error() string { return e.err.Error() }
func (e *ResolutionError) Unwrap() error { return e.err }

// IsResolution reports whether err is a Development Host naming failure.
func IsResolution(err error) bool {
	var resolved *ResolutionError
	return errors.As(err, &resolved)
}

func resolution(err error) error {
	if err == nil {
		return nil
	}
	var resolved *ResolutionError
	if errors.As(err, &resolved) {
		return err
	}
	return &ResolutionError{err: err}
}

// ResolveHost names the Development Host, in order: --host, then
// config.jsonc's default_host, then — when the SSH client configuration
// names exactly one literal Host alias — that host. A corrupt config is
// diagnosed, not bypassed. Several hosts and no default: Interactive
// prompts and pins the choice as default_host; a non-terminal run lists
// the candidates. An injected PickHost does not pin.
func ResolveHost(opts Options) (string, error) {
	opts = opts.WithDefaults()
	if opts.HostFlag != "" {
		return opts.HostFlag, nil
	}
	if host, err := PinnedHost(opts.ConfigPath); err == nil {
		return host, nil
	} else if !errors.Is(err, ErrNoHost) {
		return "", resolution(err)
	}
	hosts, err := ConfiguredHosts(SSHConfigPath(opts.SSHConfigPath))
	if err != nil {
		return "", resolution(err)
	}
	switch len(hosts) {
	case 0:
		return "", resolution(ErrNoHost)
	case 1:
		return hosts[0], nil
	default:
		picker := opts.PickHost
		if picker == nil && opts.Interactive {
			picker = pickHost
		}
		if picker != nil {
			host, err := picker(hosts, opts.Stdin, opts.Stdout)
			if err != nil {
				return "", resolution(err)
			}
			if opts.PickHost == nil {
				if err := SetDefaultHost(opts.ConfigPath, host); err == nil {
					fmt.Fprintf(opts.Stdout, "default host set to %s\n", host)
				}
			}
			return host, nil
		}
		return "", resolution(fmt.Errorf("no host selected; configured hosts: %s (pass --host, or pin one: ssh-forward default ALIAS)", strings.Join(hosts, ", ")))
	}
}

// PinnedHost returns config.jsonc's default_host. A missing file or empty
// default is ErrNoHost; a corrupt file is diagnosed.
func PinnedHost(path string) (string, error) {
	if path == "" {
		return "", ErrNoHost
	}
	config, err := LoadConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoHost
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if config.DefaultHost == "" {
		return "", ErrNoHost
	}
	return config.DefaultHost, nil
}

func pickHost(hosts []string, stdin io.Reader, stdout io.Writer) (string, error) {
	if stdout == nil {
		stdout = io.Discard
	}
	fmt.Fprintln(stdout, "Multiple Development Hosts are configured; pick one to set as the default:")
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
		for _, host := range hosts {
			if line == host {
				return host, nil
			}
		}
		fmt.Fprintln(stdout, "invalid choice; pick a number or a host alias from the list")
	}
	return "", errors.New("no host selected (pin one: ssh-forward default ALIAS)")
}

// IsTerminal reports whether the reader is an interactive terminal.
func IsTerminal(reader io.Reader) bool {
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
