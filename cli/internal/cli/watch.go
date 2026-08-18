package cli

import (
	"context"
	"fmt"

	"github.com/wangnan0916/ssh-forward/cli/internal/snapshot"
)

// runWatch streams the snapshot sequence until the context ends. Human
// mode prints each generation's status block; --json emits one
// wire-shaped snapshot per line (JSONL), the stream contract desktop and
// scripts consume.
func (a *App) runWatch(ctx context.Context, jsonOutput bool) error {
	stream, err := a.Manager.Watch(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()

	first := true
	for {
		snap, err := stream.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				// The stream ended because we were cancelled: a clean
				// stop, not a failure.
				return nil
			}
			return err
		}
		if jsonOutput {
			encoded, err := snapshot.Marshal(snap)
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Options.Stdout, string(encoded))
			continue
		}
		if !first {
			fmt.Fprintln(a.Options.Stdout)
		}
		first = false
		if err := a.writeStatusHuman(snap); err != nil {
			return err
		}
	}
}
