package cli

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func (a *App) runHostList(jsonOutput bool) error {
	hosts, err := app.ConfiguredHosts(app.SSHConfigPath(a.Options.SSHConfigPath))
	if err != nil {
		return err
	}
	if jsonOutput {
		encoded, err := json.Marshal(hosts)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Options.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintln(a.Options.Stdout, "Hosts in ~/.ssh/config:")
	if len(hosts) == 0 {
		fmt.Fprintln(a.Options.Stdout, "No Host aliases in ~/.ssh/config. Add a Host block, then: ssh-forward default ALIAS")
		return nil
	}
	selected := a.defaultHostAlias()
	for _, host := range hosts {
		if host == selected {
			fmt.Fprintf(a.Options.Stdout, "  %s  (default)\n", host)
			continue
		}
		fmt.Fprintf(a.Options.Stdout, "  %s\n", host)
	}
	if selected == "" {
		fmt.Fprintln(a.Options.Stdout, "Pin one: ssh-forward default ALIAS")
	}
	return nil
}

func (a *App) defaultHostAlias() string {
	host, err := app.PinnedHost(a.Options.ConfigPath)
	if err != nil {
		return ""
	}
	return host
}

func (a *App) runShowDefault() error {
	host, err := app.PinnedHost(a.Options.ConfigPath)
	if errors.Is(err, app.ErrNoHost) {
		fmt.Fprintln(a.Options.Stdout, "No default host.")
		fmt.Fprintln(a.Options.Stdout, "List aliases: ssh-forward host")
		fmt.Fprintln(a.Options.Stdout, "Then pin one: ssh-forward default ALIAS")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Options.Stdout, "default host: %s\n", host)
	return nil
}

func (a *App) runSetDefault(alias string) error {
	path := a.Options.ConfigPath
	if path == "" {
		return fmt.Errorf("no config path is configured")
	}
	if err := app.SetDefaultHost(path, alias); err != nil {
		return err
	}
	fmt.Fprintf(a.Options.Stdout, "default host set to %s\n", alias)
	return nil
}
