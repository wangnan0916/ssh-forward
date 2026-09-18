package app

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestProcessArgumentsPreserveExactArgv(t *testing.T) {
	args, err := processArguments(context.Background(), int32(os.Getpid()))
	require.NoError(t, err)
	require.Equal(t, os.Args, args)
}
func TestParseSSHConnectionTargets(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want HostTarget
		ok   bool
	}{
		{"direct", []string{"ssh", "user@10.0.0.1", "echo", "secret"}, HostTarget{Target: "user@10.0.0.1"}, true},
		{"options", []string{"ssh", "-NT", "-p2222", "-l", "me", "-i", "/keys/a key", "-F", "/config/ssh", "-J", "jump", "-L", "8000:localhost:80", "dev"}, HostTarget{Target: "dev", Arguments: []string{"-p", "2222", "-l", "me", "-i", "/keys/a key", "-F", "/config/ssh", "-J", "jump"}}, true},
		{"unsupported", []string{"ssh", "-o", "ProxyCommand=custom secret", "dev"}, HostTarget{Target: "dev", Diagnostic: "discovered_unsupported"}, true},
		{"relative path", []string{"ssh", "-i", "key", "dev"}, HostTarget{Target: "dev", Diagnostic: "discovered_unsupported"}, true},
		{"config evaluation", []string{"ssh", "-G", "dev"}, HostTarget{}, false},
		{"control", []string{"ssh", "-O", "check", "dev"}, HostTarget{}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseSSHProcess(test.args)
			require.Falsef(t, ok != test.ok || !reflect.DeepEqual(got, test.want), "got %+v %v, want %+v %v", got, ok, test.want, test.ok)
		})
	}
	a, _ := parseSSHProcess([]string{"ssh", "-p", "2222", "dev"})
	b, _ := parseSSHProcess([]string{"ssh", "-p", "2223", "dev"})
	require.False(t, targetID(a) == targetID(b), "distinct connections merged")
}
func TestDiscoveryExcludesOtherUsersAndProductProcesses(t *testing.T) {
	input := []hostProcess{
		{10, 1, 501, "ssh-forward"}, {11, 10, 501, "ssh"}, {12, 11, 501, "ssh"},
		{20, 1, 501, "ssh"}, {21, 1, 502, "ssh"}, {30, 1, 501, "bash"}, {31, 30, 501, "ssh"},
	}
	got := discoverProcesses(input, 10, 501, func(pid int32) ([]string, error) {
		if pid == 20 {
			return []string{"ssh", "user@direct"}, nil
		}
		if pid == 31 {
			return []string{"ssh", "dev"}, nil
		}
		t.Fatalf("read excluded process %d", pid)
		return nil, nil
	})
	require.Falsef(t, len(got) != 2 || got["user@direct"].Target == "" || got["dev"].Target == "", "unexpected discovery: %+v", got)
}
func TestGlobalRulesAndDiscoverySurviveRestartAndIgnore(t *testing.T) {
	ctx := context.Background()
	pool, _ := testPool(t, configFile{Hosts: map[string]HostTarget{"manual": {Target: "manual"}}, GlobalForwards: []core.RememberedForward{{RemotePort: 8080}}, GlobalWorkingDirectoryRules: []string{"/other/**"}})
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
func TestSchemaFiveMigrationKeepsPublishedAndScopedRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	require.NoError(t, writeAtomic(path, []byte(`{"schema_version":5,"remembered_forwards":{"dev":[{"remote_port":3000,"local_port":3000}]},"published_forwards":{"dev":[{"local_port":9222}]},"working_directory_rules":{"dev":["/work/**"]}}`)))
	if _, err := EditRememberedForward(path, "", &core.RememberedForward{RemotePort: 8080}, true); err != nil {
		t.Fatal(err)
	}
	dev, err := HostIntent(path, "dev")
	require.NoError(t, err)
	other, err := HostIntent(path, "other")
	require.NoError(t, err)
	require.Falsef(t, len(dev.PublishedForwards) != 1 || len(dev.RememberedForwards) != 1 || len(dev.WorkingDirectoryRules) != 1 || len(dev.AutoForwards) != 1, "lost old rules: %+v", dev)
	require.Falsef(t, len(other.PublishedForwards) != 0 || len(other.RememberedForwards) != 0 || len(other.WorkingDirectoryRules) != 0 || len(other.AutoForwards) != 1, "old rules broadened: %+v", other)
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

func TestOrphanedProductMasterIsNotDiscovered(t *testing.T) {
	_, ok := parseSSHProcess([]string{"ssh", "-M", "-N", "-S", "master-abc123", "dev"})
	require.False(t, ok, "product master fed back into discovery")
}

func TestProcessArgumentsPreserveBoundaries(t *testing.T) {
	if os.Getenv("SSH_FORWARD_ARGV_PROBE") == "1" {
		_, _ = os.Stdout.WriteString("ready\n")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
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
			results <- rememberDiscovered(context.Background(), path, map[string]HostTarget{name: {Target: name}})
		}()
	}
	for range 2 {
		require.NoError(t, <-results)
	}
	targets, err := loadDiscovered(path)
	require.Falsef(t, err != nil || len(targets) != 2, "concurrent discoveries lost: %+v, %v", targets, err)
}

func TestTerminalRejectsCharacterDevice(t *testing.T) {
	file, err := os.Open(os.DevNull)
	require.NoError(t, err)
	defer file.Close()
	require.False(t, IsTerminal(file), "null device treated as terminal")
}
