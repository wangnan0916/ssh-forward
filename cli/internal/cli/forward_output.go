package cli

import (
	"fmt"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func (a *App) writePublished(
	jsonOutput, adding, changed bool,
	host string,
	forward core.PublishedForward,
) error {
	if jsonOutput {
		return a.writeJSON(map[string]any{
			mutationJSONKey(adding): changed, "host": host,
			"local_port": forward.LocalPort, "remote_port": forward.RemotePort,
		})
	}
	switch {
	case adding && changed:
		fmt.Fprintf(
			a.Options.Stdout,
			"Publishing local 127.0.0.1:%d at %s 127.0.0.1:%d.\n",
			forward.LocalPort, host, forward.RemotePort,
		)
	case adding:
		fmt.Fprintf(a.Options.Stdout, "Local port %d is already published for %s.\n", forward.LocalPort, host)
	default:
		fmt.Fprintf(a.Options.Stdout, "Stopped publishing local port %d for %s.\n", forward.LocalPort, host)
	}
	return nil
}

func (a *App) writeRemember(
	jsonOutput, adding, changed bool,
	host string,
	forward core.RememberedForward,
) error {
	if jsonOutput {
		output := map[string]any{
			mutationJSONKey(adding): changed,
			"host":                  host,
			"remote_port":           forward.RemotePort,
		}
		if adding {
			output["local_port"] = forward.LocalPort
			output["allow_fallback"] = forward.AllowFallback
		}
		return a.writeJSON(output)
	}
	switch {
	case adding && changed && forward.AllowFallback:
		fmt.Fprintf(
			a.Options.Stdout,
			"Remembered remote %d for %s (prefers 127.0.0.1:%d; falls back if busy).\n",
			forward.RemotePort, host, forward.LocalPort,
		)
	case adding && changed:
		fmt.Fprintf(
			a.Options.Stdout,
			"Remembered remote %d at 127.0.0.1:%d for %s.\n",
			forward.RemotePort, forward.LocalPort, host,
		)
	case adding:
		fmt.Fprintf(a.Options.Stdout, "Already remembered remote %d for %s.\n", forward.RemotePort, host)
	default:
		fmt.Fprintf(a.Options.Stdout, "Forgot remote %d for %s.\n", forward.RemotePort, host)
	}
	return nil
}

func (a *App) writeRememberWorkingDirectory(jsonOutput, adding, changed bool, host, pattern string) error {
	if jsonOutput {
		return a.writeJSON(map[string]any{
			mutationJSONKey(adding):  changed,
			"host":                   host,
			"working_directory_rule": pattern,
		})
	}
	switch {
	case adding && changed:
		fmt.Fprintf(a.Options.Stdout, "Remembered working-directory glob %s for %s.\n", pattern, host)
	case adding:
		fmt.Fprintf(a.Options.Stdout, "Already remembered working-directory glob %s for %s.\n", pattern, host)
	default:
		fmt.Fprintf(a.Options.Stdout, "Forgot working-directory glob %s for %s.\n", pattern, host)
	}
	return nil
}
