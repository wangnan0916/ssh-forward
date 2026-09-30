package app

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestGlobalRulesAndDiscoverySurviveRestartAndIgnore(t *testing.T) {
	ctx := t.Context()
	pool, _ := testPool(t, configuration{
		Hosts: map[string]HostTarget{"manual": {Target: "manual"}},
		Rules: map[string]*scopeRules{
			"": new(scopeRules{Forwards: []core.RememberedForward{{RemotePort: 8080}}, Directories: []string{"/other/**"}}),
		},
	})
	require.NoError(t, writeJSONC(discoveryPath(pool.configPath), map[string]HostTarget{"found": {Target: "found"}}))
	require.NoError(t, pool.reload(ctx, ""))
	for _, host := range []string{"manual", "found"} {
		awaitPoolStatus(t, pool, host, allPoolForwardsActive(1))
	}
	require.NoError(t, EditHost(pool.configPath, "found", nil, true))
	require.NoError(t, pool.reload(ctx, ""))
	require.Nil(t, pool.lookup("found"), "ignored host still running")
	require.NoError(t, pool.reload(ctx, "found"))
	require.Nil(t, pool.lookup("found"), "ignored host was rediscovered")
	remembered, ignored, err := HostList(pool.configPath)
	require.NoError(t, err)
	require.False(t, remembered["found"].Target != "found" || len(ignored) != 1, "remembered state lost")
	require.NoError(t, EditHost(pool.configPath, "found", nil, false))
	require.NoError(t, pool.reload(ctx, ""))
	awaitPoolStatus(t, pool, "found", allPoolForwardsActive(1))
}
func TestEnableDestinationDoesNotInventPlainConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	target := HostTarget{Target: "dev", Arguments: []string{"-p", "2222"}}
	require.NoError(t, writeJSONC(discoveryPath(path), map[string]HostTarget{targetID(target): target}))
	require.NoError(t, EditHost(path, "dev", nil, true))
	require.NoError(t, EditHost(path, "dev", nil, false))
	hosts, ignored, err := HostList(path)
	require.NoError(t, err)
	require.Falsef(t, len(hosts) != 1 || len(ignored) != 0 || hosts[targetID(target)].Target != "dev", "invented target: %+v, %v", hosts, ignored)
}

func TestProcessArgumentsPreserveBoundaries(t *testing.T) {
	if os.Getenv("SSH_FORWARD_ARGV_PROBE") == "1" {
		_, _ = os.Stdout.WriteString("ready\n")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessArgumentsPreserveBoundaries$", "--", "a key with spaces", "", "quote'\"", "你好", "last-argument")
	cmd.Env = append(os.Environ(), "SSH_FORWARD_ARGV_PROBE=1", "SSH_FORWARD_ARGV_SECRET=must-not-be-argv")
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	output, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() {
		_ = input.Close()
		if err := cmd.Wait(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := bufio.NewReader(output).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	args, err := processArguments(ctx, int32(cmd.Process.Pid))
	require.NoError(t, err)
	require.Equal(t, cmd.Args, args)
}

func TestDiscoveredRegistryLockCancellationAndMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	lock := flock.New(discoveryPath(path) + ".lock")
	require.NoError(t, lock.Lock())
	defer lock.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, rememberDiscovered(ctx, path, map[string]HostTarget{"blocked": {Target: "blocked"}}), context.DeadlineExceeded)
	if _, err := os.Stat(discoveryPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrote without lock: %v", err)
	}
	info, err := os.Stat(discoveryPath(path) + ".lock")
	require.NoError(t, err)
	require.EqualValuesf(t, 0600, info.Mode().Perm(), "lock permissions: %v", info.Mode())
	require.NoError(t, lock.Unlock())
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func() {
			results <- rememberDiscovered(t.Context(), path, map[string]HostTarget{name: {Target: name}})
		}()
	}
	for range 2 {
		require.NoError(t, <-results)
	}
	targets, err := loadDiscovered(path)
	require.Falsef(t, err != nil || len(targets) != 2, "concurrent discoveries lost: %+v, %v", targets, err)
}
