package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

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
