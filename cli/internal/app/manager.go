package app

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const (
	managerServiceName = "com.wangnan0916.ssh-forward"
	managerStartWait   = 5 * time.Second
)

// Session is the service API. Configuration is the only source of intent.
type Session interface {
	AllStatuses(context.Context) ([]core.Status, error)
	Reload(context.Context, string) error
	Close(context.Context) error
}

// Options are the local files, host inputs, and command streams used by the
// CLI and its per-user Manager.
type Options struct {
	Layout        Layout
	HostFlag      string
	SSHConfigPath string
	ConfigPath    string
	Version       string
	Interactive   bool
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
}

func (o Options) WithDefaults() Options {
	if o.Layout.Dir == "" {
		o.Layout = DefaultLayout()
	} else {
		filled := layoutForDir(o.Layout.Dir)
		o.Layout.Config = cmp.Or(o.Layout.Config, filled.Config)
		o.Layout.Socket = cmp.Or(o.Layout.Socket, filled.Socket)
	}
	o.ConfigPath = cmp.Or(o.ConfigPath, o.Layout.Config)
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	return o
}

// Connect opens the service session, installing or repairing the user service
// when needed. An explicit host adds a runtime alongside remembered hosts.
func Connect(ctx context.Context, opts Options) (Session, error) {
	opts = opts.WithDefaults()
	client, dialErr := dialManager(ctx, opts.Layout.Socket, opts.Version)
	if dialErr != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err := os.MkdirAll(opts.Layout.Dir, 0700); err != nil {
			return nil, err
		}
		svc, err := newManagerService(ctx, opts, "")
		if err != nil {
			return nil, err
		}
		if socketLive(opts.Layout.Socket) {
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
	}
	if err := client.Reload(ctx, opts.HostFlag); err != nil {
		_ = client.Close(context.Background())
		return nil, err
	}
	return client, nil
}

// Serve runs the Manager in the current process. Installed service definitions
// invoke this hidden command.
func Serve(ctx context.Context, opts Options) error {
	opts = opts.WithDefaults()
	host := opts.HostFlag
	svc, err := newManagerService(ctx, opts, host)
	if err != nil {
		return err
	}
	return svc.Run()
}
