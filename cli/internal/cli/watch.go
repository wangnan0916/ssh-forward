package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"ssh-forward/cli/internal/jsonrpc"
)

// runWatch streams the snapshot sequence until the context ends. Human
// mode prints each generation's status block; --json emits one
// wire-shaped snapshot per line (JSONL), the stream contract desktop and
// scripts consume.
func (a *App) runWatch(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("watch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	jsonOutput := flags.Bool("json", false, "emit one wire-shaped snapshot per line")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("watch takes no positional arguments")
	}
	stream, err := a.Manager.Watch(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()

	first := true
	for {
		snapshot, err := stream.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// The stream ended because we were cancelled: a clean
				// stop, not a failure.
				return nil
			}
			return err
		}
		if *jsonOutput {
			encoded, err := jsonrpc.MarshalSnapshot(snapshot)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Stdout, string(encoded))
			continue
		}
		if !first {
			fmt.Fprintln(a.Stdout)
		}
		first = false
		if err := a.writeStatusHuman(snapshot); err != nil {
			return err
		}
	}
}
