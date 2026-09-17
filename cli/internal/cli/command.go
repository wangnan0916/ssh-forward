package cli

import (
	"context"
	"fmt"

	"github.com/alecthomas/kong"

	"github.com/wangnan0916/ssh-forward/cli/internal/app"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// The grammar is the command surface. Run methods live with their domain;
// there is no command-name switch or second tree of flag registrations.
type commands struct {
	Host      string           `help:"SSH target: alias, hostname, IP, or user@host; omit for global rules or all-host status."`
	SSHConfig string           `help:"SSH client config file (default: ~/.ssh/config)."`
	Version   kong.VersionFlag `help:"Print version and exit."`
	Add       addCommand       `cmd:"" help:"Remember a global listener port or working-directory glob."`
	Remove    removeCommand    `cmd:"" help:"Forget a remembered port or working-directory glob."`
	Publish   publishCommand   `cmd:"" help:"Publish a local port on the Development Host; requires --host."`
	Unpublish unpublishCommand `cmd:"" help:"Stop publishing a local port; requires --host."`
	Status    statusCommand    `cmd:"" help:"Show listeners and forwards for all hosts."`
	Doctor    doctorCommand    `cmd:"" help:"Diagnose configuration, SSH, and Manager health without changing it."`
	Hosts     hostCommands     `cmd:"" name:"host" help:"Manage remembered and discovered SSH hosts."`
	Uninstall uninstallCommand `cmd:"" help:"Remove the background manager; keep configuration."`
	Manager   struct {
		Serve serveCommand `cmd:""`
	} `cmd:"" hidden:""`
	Help helpCommand `cmd:"" hidden:""`
}

type jsonOption struct {
	JSON bool `help:"Emit JSON."`
}
type importOptions struct {
	jsonOption
	Port string  `arg:"" optional:"" name:"PORT"`
	Pwd  *string `help:"Absolute glob for remote process working directories."`
}
type addCommand struct {
	importOptions
	Local *uint16 `help:"Strict local port (default: remote port with fallback)."`
}
type removeCommand struct{ importOptions }
type publishOptions struct {
	jsonOption
	Local string `arg:"" name:"LOCAL"`
}
type publishCommand struct {
	publishOptions
	Remote *uint16 `help:"Development Host port (default: local port)."`
}
type unpublishCommand struct{ publishOptions }
type statusCommand struct {
	jsonOption
	Watch bool `help:"Refresh until interrupted."`
}
type doctorCommand struct{ jsonOption }
type hostCommands struct {
	jsonOption
	List     hostListCommand     `cmd:"" default:"withargs" help:"List remembered and discovered hosts."`
	Add      hostAddCommand      `cmd:"" help:"Remember an SSH target for global rules."`
	Ignore   hostToggleCommand   `cmd:"" help:"Stop monitoring a host."`
	Enable   hostToggleCommand   `cmd:"" help:"Re-enable a host."`
	Discover hostDiscoverCommand `cmd:"" help:"Remember active local SSH targets."`
	Aliases  hostAliasesCommand  `cmd:"" help:"List SSH config candidates without connecting."`
}
type hostAddCommand struct {
	Name     string `arg:"" name:"HOST"`
	Target   string `help:"SSH destination when different from the stored name."`
	Port     int    `help:"SSH port."`
	User     string `help:"SSH username."`
	Identity string `help:"Absolute private-key path."`
	Jump     string `help:"SSH jump host."`
}
type hostToggleCommand struct {
	Name string `arg:"" name:"HOST"`
}
type hostListCommand struct{}
type hostDiscoverCommand struct{}
type hostAliasesCommand struct{}
type uninstallCommand struct{}
type serveCommand struct{}
type helpCommand struct {
	Command []string `arg:"" optional:""`
}

func (c *addCommand) Run(a *App, ctx context.Context) error {
	return c.importOptions.edit(a, ctx, c.Local, true)
}
func (c *removeCommand) Run(a *App, ctx context.Context) error {
	return c.importOptions.edit(a, ctx, nil, false)
}
func (c *publishCommand) Run(a *App, ctx context.Context) error {
	return c.publishOptions.edit(a, ctx, c.Remote, true)
}
func (c *unpublishCommand) Run(a *App, ctx context.Context) error {
	return c.publishOptions.edit(a, ctx, nil, false)
}

func (c importOptions) edit(a *App, ctx context.Context, local *uint16, adding bool) error {
	name := "add"
	if !adding {
		name = "remove"
	}
	if c.Pwd != nil {
		if c.Port != "" || local != nil {
			return UsageError(fmt.Errorf("%s accepts either PORT or --pwd GLOB, not both or --local", name))
		}
		return a.rememberWorkingDirectory(ctx, *c.Pwd, adding, c.JSON)
	}
	port, err := requirePort(name, "remote", c.Port)
	if err != nil {
		return UsageError(err)
	}
	forward := core.RememberedForward{RemotePort: port}.WithDefaults()
	if local != nil {
		if *local == 0 {
			return UsageError(fmt.Errorf("add --local requires a port 1..65535"))
		}
		forward.LocalPort, forward.AllowFallback = *local, false
	}
	return a.rememberForward(ctx, forward, adding, c.JSON)
}

func (c publishOptions) edit(a *App, ctx context.Context, remote *uint16, adding bool) error {
	name := "publish"
	if !adding {
		name = "unpublish"
	}
	if a.Options.HostFlag == "" {
		return UsageError(fmt.Errorf("publish and unpublish require --host TARGET"))
	}
	port, err := requirePort(name, "local", c.Local)
	if err != nil {
		return UsageError(err)
	}
	forward := core.PublishedForward{LocalPort: port}.WithDefaults()
	if remote != nil {
		if *remote == 0 {
			return UsageError(fmt.Errorf("publish --remote requires a port 1..65535"))
		}
		forward.RemotePort = *remote
	}
	return a.publishForward(ctx, forward, adding, c.JSON)
}

func (*uninstallCommand) Run(a *App) error {
	if err := app.Uninstall(a.Options.Layout); err != nil {
		return err
	}
	_, err := fmt.Fprintln(a.Options.Stdout, "Background manager removed; configuration kept.")
	return err
}
func (*serveCommand) Run(a *App, ctx context.Context) error { return app.Serve(ctx, a.Options) }
func (c *helpCommand) Run(a *App, ctx context.Context) error {
	return a.Run(ctx, append(c.Command, "--help"))
}
