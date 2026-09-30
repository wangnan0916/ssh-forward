package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollapseSameHostsKeepsConfigAlias(t *testing.T) {
	targets := map[string]HostTarget{
		"ubuntu":         {Target: "ubuntu"},
		"shampoo@ubuntu": {Target: "shampoo@ubuntu"},
		"git@github.com": {Target: "git@github.com"},
	}
	resolve := func(_ context.Context, _ string, target HostTarget) (string, string, bool) {
		switch target.Target {
		case "ubuntu", "shampoo@ubuntu":
			return "shampoo", "100.83.29.59", true
		case "git@github.com":
			return "git", "github.com", true
		default:
			return "", "", false
		}
	}
	config := filepath.Join(t.TempDir(), "ssh_config")
	require.NoError(t, os.WriteFile(config, []byte("Host ubuntu\n"), 0o600))
	collapsed := collapseSameHosts(t.Context(), config, targets, map[string]HostTarget{"shampoo@ubuntu": {Target: "shampoo@ubuntu"}}, resolve)
	require.Equal(t, map[string]HostTarget{
		"ubuntu":         {Target: "ubuntu"},
		"git@github.com": {Target: "git@github.com"},
	}, collapsed)

	recorded := collapseSameHosts(t.Context(), filepath.Join(t.TempDir(), "missing"), map[string]HostTarget{
		"dev": {Target: "dev"}, "server": {Target: "server"},
	}, map[string]HostTarget{"server": {Target: "server"}}, func(_ context.Context, _ string, _ HostTarget) (string, string, bool) {
		return "me", "10.0.0.8", true
	})
	require.Equal(t, map[string]HostTarget{"server": {Target: "server"}}, recorded)
}

func TestResolveUserHostReadsUserAndHostname(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	dir := t.TempDir()
	config := filepath.Join(dir, "ssh_config")
	require.NoError(t, os.WriteFile(config, []byte("Host ubuntu\n  HostName 127.0.0.1\n  Port 2222\n  User shampoo\n"), 0o600))
	ctx := t.Context()
	aliasUser, aliasHost, ok := resolveUserHost(ctx, config, HostTarget{Target: "ubuntu"})
	require.True(t, ok)
	user, host, ok := resolveUserHost(ctx, config, HostTarget{Target: "shampoo@ubuntu"})
	require.True(t, ok)
	require.Equal(t, aliasUser, user)
	require.Equal(t, aliasHost, host)
	require.Equal(t, "shampoo", user)
	require.Equal(t, "127.0.0.1", host)
}
