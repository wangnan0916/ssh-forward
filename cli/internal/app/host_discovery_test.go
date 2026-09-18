package app

import (
	"bufio"
	"context"
	"errors"
	"github.com/gofrs/flock"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

func TestProcessArgumentsPreserveExactArgv(t *testing.T) {
	args, err := processArguments(context.Background(), int32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(args, os.Args) {
		t.Fatalf("argv mismatch: %s", cmp.Diff(os.Args, args))
	}
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
			if ok != test.ok || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %+v %v, want %+v %v", got, ok, test.want, test.ok)
			}
		})
	}
	a, _ := parseSSHProcess([]string{"ssh", "-p", "2222", "dev"})
	b, _ := parseSSHProcess([]string{"ssh", "-p", "2223", "dev"})
	if targetID(a) == targetID(b) {
		t.Fatal("distinct connections merged")
	}
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
	if len(got) != 2 || got["user@direct"].Target == "" || got["dev"].Target == "" {
		t.Fatalf("unexpected discovery: %+v", got)
	}
}
func TestGlobalRulesAndDiscoverySurviveRestartAndIgnore(t *testing.T) {
	ctx := context.Background()
	pool, _ := testPool(t, configFile{Hosts: map[string]HostTarget{"manual": {Target: "manual"}}, GlobalForwards: []core.RememberedForward{{RemotePort: 8080}}, GlobalWorkingDirectoryRules: []string{"/other/**"}})
	if err := writeJSONC(discoveryPath(pool.configPath), map[string]HostTarget{"found": {Target: "found"}}); err != nil {
		t.Fatal(err)
	}
	if err := pool.reload(ctx, ""); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"manual", "found"} {
		awaitPoolStatus(t, pool, host, allPoolForwardsActive(1))
	}
	if err := SetHost(pool.configPath, "found", HostTarget{}, true); err != nil {
		t.Fatal(err)
	}
	if err := pool.reload(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if pool.lookup("found") != nil {
		t.Fatal("ignored host still running")
	}
	if err := pool.reload(ctx, "found"); err != nil {
		t.Fatal(err)
	}
	if pool.lookup("found") != nil {
		t.Fatal("ignored host was rediscovered")
	}
	remembered, ignored, err := HostList(pool.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if remembered["found"].Target != "found" || len(ignored) != 1 {
		t.Fatal("remembered state lost")
	}
	if err := SetHost(pool.configPath, "found", remembered["found"], false); err != nil {
		t.Fatal(err)
	}
	if err := pool.reload(ctx, ""); err != nil {
		t.Fatal(err)
	}
	awaitPoolStatus(t, pool, "found", allPoolForwardsActive(1))
}
func TestSchemaFiveMigrationKeepsPublishedAndScopedRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	if err := writeAtomic(path, []byte(`{"schema_version":5,"remembered_forwards":{"dev":[{"remote_port":3000,"local_port":3000}]},"published_forwards":{"dev":[{"local_port":9222}]},"working_directory_rules":{"dev":["/work/**"]}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := SetRememberedForward(path, "", core.RememberedForward{RemotePort: 8080}); err != nil {
		t.Fatal(err)
	}
	dev, err := HostIntent(path, "dev")
	if err != nil {
		t.Fatal(err)
	}
	other, err := HostIntent(path, "other")
	if err != nil {
		t.Fatal(err)
	}
	if len(dev.PublishedForwards) != 1 || len(dev.RememberedForwards) != 1 || len(dev.WorkingDirectoryRules) != 1 || len(dev.AutoForwards) != 1 {
		t.Fatalf("lost old rules: %+v", dev)
	}
	if len(other.PublishedForwards) != 0 || len(other.RememberedForwards) != 0 || len(other.WorkingDirectoryRules) != 0 || len(other.AutoForwards) != 1 {
		t.Fatalf("old rules broadened: %+v", other)
	}
}

func TestEnableDestinationDoesNotInventPlainConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	target := HostTarget{Target: "dev", Arguments: []string{"-p", "2222"}}
	if err := writeJSONC(discoveryPath(path), map[string]HostTarget{targetID(target): target}); err != nil {
		t.Fatal(err)
	}
	if err := SetHost(path, "dev", HostTarget{}, true); err != nil {
		t.Fatal(err)
	}
	if err := EnableHost(path, "dev"); err != nil {
		t.Fatal(err)
	}
	hosts, ignored, err := HostList(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 1 || len(ignored) != 0 || hosts[targetID(target)].Target != "dev" {
		t.Fatalf("invented target: %+v, %v", hosts, ignored)
	}
}

func TestOrphanedProductMasterIsNotDiscovered(t *testing.T) {
	_, ok := parseSSHProcess([]string{"ssh", "-M", "-N", "-S", "master-abc123", "dev"})
	if ok {
		t.Fatal("product master fed back into discovery")
	}
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
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(cmd.Args, args); diff != "" {
		t.Fatalf("argument boundaries changed: %s", diff)
	}
}

func TestDiscoveredRegistryLockCancellationAndMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.jsonc")
	lock := flock.New(discoveryPath(path) + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := rememberDiscovered(ctx, path, map[string]HostTarget{"blocked": {Target: "blocked"}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock timeout: %v", err)
	}
	if _, err := os.Stat(discoveryPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrote without lock: %v", err)
	}
	info, err := os.Stat(discoveryPath(path) + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("lock permissions: %v", info.Mode())
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, name := range []string{"one", "two"} {
		go func() {
			results <- rememberDiscovered(context.Background(), path, map[string]HostTarget{name: {Target: name}})
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	targets, err := loadDiscovered(path)
	if err != nil || len(targets) != 2 {
		t.Fatalf("concurrent discoveries lost: %+v, %v", targets, err)
	}
}

func TestTerminalRejectsCharacterDevice(t *testing.T) {
	file, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if IsTerminal(file) {
		t.Fatal("null device treated as terminal")
	}
}
