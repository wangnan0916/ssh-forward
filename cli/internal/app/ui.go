package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	envUIServe     = "SSH_FORWARD_UI_SERVE"
	envUIHost      = "SSH_FORWARD_UI_HOST"
	envUIPolicies  = "SSH_FORWARD_UI_POLICIES"
	envUISSHConfig = "SSH_FORWARD_UI_SSH_CONFIG"
)

var (
	// ErrUINotRunning reports that this user's WebUI pid/url is not live.
	ErrUINotRunning = errors.New("WebUI is not running")
)

// TakeUIServeEnv reports whether this process is the autospawned WebUI
// child and copies its Options encoding into opts. The child enters the
// loopback HTTP serve path without parsing a Cobra command tree.
func TakeUIServeEnv(opts *Options) bool {
	return takeServeEnv(envUIServe, envUIHost, envUIPolicies, envUISSHConfig, opts)
}

// LiveUIURL returns the loopback page URL when the recorded UI pid is live.
func LiveUIURL(layout Layout) (string, error) {
	pid, err := ReadPIDFile(layout.UIPID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrUINotRunning
		}
		return "", err
	}
	if !PIDAlive(pid) {
		return "", ErrUINotRunning
	}
	raw, err := os.ReadFile(layout.UIURL)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrUINotRunning
		}
		return "", err
	}
	url := strings.TrimSpace(string(raw))
	if url == "" {
		return "", ErrUINotRunning
	}
	return url, nil
}

// StopUI ends this user's WebUI process if we wrote its pid and it is still
// alive. Missing or dead pid files are not running.
func StopUI(layout Layout) error {
	pid, err := ReadPIDFile(layout.UIPID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrUINotRunning
		}
		return err
	}
	if !PIDAlive(pid) {
		RemoveUIFiles(layout)
		return ErrUINotRunning
	}
	if err := TerminatePID(pid); err != nil {
		return fmt.Errorf("stop WebUI: %w", err)
	}
	RemoveUIFiles(layout)
	return nil
}

// StartUI returns the live loopback URL, spawning the WebUI child when none
// is running. The child is encoded as environment, not `ui serve` argv.
func StartUI(ctx context.Context, opts Options) (string, error) {
	opts = opts.WithDefaults()
	if url, err := LiveUIURL(opts.Layout); err == nil {
		return url, nil
	}
	if _, err := ResolveHost(opts); err != nil {
		return "", err
	}
	pid, err := ReadPIDFile(opts.Layout.UIPID)
	if err != nil || !PIDAlive(pid) {
		pid, err = spawnUI(opts)
		if err != nil {
			return "", err
		}
	}
	return WaitForUI(ctx, opts.Layout, 10*time.Second, pid)
}

// WaitForUI polls until LiveUIURL succeeds, the child dies, or timeout.
func WaitForUI(ctx context.Context, layout Layout, timeout time.Duration, childPID int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if url, err := LiveUIURL(layout); err == nil {
			return url, nil
		}
		if childPID > 0 && !PIDAlive(childPID) {
			return "", uiStartError(layout, "WebUI failed to start")
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", uiStartError(layout, fmt.Sprintf("WebUI did not start within %s", timeout))
			}
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func spawnUI(opts Options) (int, error) {
	return startServeChild("SSH_FORWARD_UI_BINARY", serveChildEnv(envUIServe, envUIHost, opts.HostFlag, envUIPolicies, envUISSHConfig, opts), opts.Layout.Dir, opts.Layout.UILog)
}

func RemoveUIFiles(layout Layout) {
	_ = os.Remove(layout.UIPID)
	_ = os.Remove(layout.UIURL)
}

func uiStartError(layout Layout, fallback string) error {
	if line := lastLogLine(layout.UILog); line != "" {
		return fmt.Errorf("WebUI failed to start: %s (see %q)", line, layout.UILog)
	}
	return fmt.Errorf("%s (see %q)", fallback, layout.UILog)
}
