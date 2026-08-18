package cli

import (
	"encoding/json"
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
	fmt.Fprintln(a.Options.Stdout, "Configured hosts:")
	if len(hosts) == 0 {
		fmt.Fprintln(a.Options.Stdout, "  (none)")
		return nil
	}
	selected := a.defaultHostAlias()
	for _, host := range hosts {
		marker := "  "
		if host == selected {
			marker = "* "
		}
		fmt.Fprintf(a.Options.Stdout, "%s%s\n", marker, host)
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
