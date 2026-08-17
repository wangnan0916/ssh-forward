package cli

import (
	"encoding/json"
	"fmt"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
)

func (a *App) runHostList(jsonOutput bool) error {
	hosts, err := app.ConfiguredHosts(app.SSHConfigPath(a.SSHConfigPath))
	if err != nil {
		return err
	}
	if jsonOutput {
		encoded, err := json.Marshal(hosts)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Stdout, string(encoded))
		return nil
	}
	fmt.Fprintln(a.Stdout, "Configured hosts:")
	if len(hosts) == 0 {
		fmt.Fprintln(a.Stdout, "  (none)")
		return nil
	}
	selected := a.defaultHostAlias()
	for _, host := range hosts {
		marker := "  "
		if host == selected {
			marker = "* "
		}
		fmt.Fprintf(a.Stdout, "%s%s\n", marker, host)
	}
	return nil
}

func (a *App) defaultHostAlias() string {
	if a.ConfigPath == "" {
		return ""
	}
	config, err := app.LoadConfig(a.ConfigPath)
	if err != nil {
		return ""
	}
	return config.DefaultHost
}

func (a *App) runSetDefault(alias string) error {
	path := a.ConfigPath
	if path == "" {
		return fmt.Errorf("no config path is configured")
	}
	if err := app.SetDefaultHost(path, alias); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "default host set to %s\n", alias)
	return nil
}
