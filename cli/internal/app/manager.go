package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	managerServiceName = "com.wangnan0916.ssh-forward"
	managerStartWait   = 5 * time.Second
)

// HostPicker chooses one Development Host alias from a candidate list.
type HostPicker func(hosts []string, stdin io.Reader, stdout io.Writer) (string, error)

// Options are the local files, host inputs, and command streams used by the
// CLI and its per-user Manager.
type Options struct {
	Layout        Layout
	HostFlag      string
	SSHConfigPath string
	ConfigPath    string
	Version       string
	Interactive   bool
	PickHost      HostPicker
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

func (o Options) WithDefaults() Options {
	if o.Layout.Dir == "" {
		o.Layout = DefaultLayout()
	} else {
		if o.Layout.Config == "" {
			o.Layout.Config = filepath.Join(o.Layout.Dir, "config.jsonc")
		}
		if o.Layout.Socket == "" {
			o.Layout.Socket = filepath.Join(o.Layout.Dir, "manager.sock")
		}
	}
	if o.ConfigPath == "" {
		o.ConfigPath = o.Layout.Config
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	return o
}

// Connect returns the Manager for the selected host. It installs or repairs
// the user service when needed and switches a running Manager whose host no
// longer matches the current selection.
func Connect(ctx context.Context, opts Options) (core.Manager, error) {
	opts = opts.WithDefaults()
	host, err := ResolveHost(opts)
	if err != nil {
		return nil, err
	}
	intent, err := HostIntent(opts.ConfigPath, host)
	if err != nil {
		return nil, err
	}

	client, dialErr := dialManager(ctx, opts.Layout.Socket, opts.Version)
	replace := dialErr != nil && socketLive(opts.Layout.Socket)
	if dialErr == nil {
		status, statusErr := client.Status(ctx)
		if statusErr == nil && status.Host == core.HostAlias(host) {
			if managerMatches(status, host, intent) {
				return client, nil
			}
			updateErr := client.UpdateIntent(ctx, intent)
			if updateErr == nil {
				return client, nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				_ = client.Close(context.Background())
				return nil, ctxErr
			}
		}
		_ = client.Close(context.Background())
		replace = true
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err := os.MkdirAll(opts.Layout.Dir, 0o700); err != nil {
		return nil, err
	}

	svc, err := newManagerService(ctx, opts, host)
	if err != nil {
		return nil, err
	}
	if replace {
		err = reinstallService(svc, opts.Layout)
	} else {
		err = ensureService(svc, opts.Layout)
	}
	if err != nil {
		return nil, fmt.Errorf("could not start the manager: %w", err)
	}
	client, err = waitManager(ctx, opts.Layout.Socket, opts.Version, managerStartWait)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func managerMatches(status core.Status, host string, intent core.ForwardingIntent) bool {
	if status.Host != core.HostAlias(host) || !slices.Equal(status.WorkingDirectoryRules, intent.WorkingDirectoryRules) {
		return false
	}
	rememberedForwards := make([]core.RememberedForward, 0, len(status.Forwards))
	for _, forward := range status.Forwards {
		if !forward.Automatic {
			rememberedForwards = append(rememberedForwards, core.RememberedForward{
				RemotePort:    forward.RemotePort,
				LocalPort:     forward.PreferredLocalPort,
				AllowFallback: forward.AllowFallback,
			})
		}
	}
	return slices.Equal(rememberedForwards, intent.RememberedForwards)
}

// Serve runs the Manager in the current process. Installed service definitions
// invoke this hidden command.
func Serve(ctx context.Context, opts Options) error {
	opts = opts.WithDefaults()
	host, err := ResolveHost(opts)
	if err != nil {
		return err
	}
	svc, err := newManagerService(ctx, opts, host)
	if err != nil {
		return err
	}
	return svc.Run()
}
