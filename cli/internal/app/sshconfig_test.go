package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfiguredHostsPreservesLiteralOrderAcrossIncludesAndCycles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	extra := filepath.Join(dir, "extra.conf")
	// The included file points back to the parent; duplicate includes and Host
	// declarations must neither recurse forever nor duplicate candidates.
	require.NoError(t, writeTextFile(extra, "Host extra ubuntu\nInclude "+path+"\n"))
	require.NoError(t, writeTextFile(path, `# personal hosts
Host ubuntu devbox
    User dev
Host *.example.com *
    User ignored
host casesensitive
Include `+extra+"\nInclude "+filepath.Join(dir, "*.conf")+"\nInclude missing-file.conf\n"))
	hosts, err := ConfiguredHosts(path)
	require.NoError(t, err)
	require.Equal(t, []string{"ubuntu", "devbox", "casesensitive", "extra"}, hosts)
	require.NoError(t, os.Remove(path))
	hosts, err = ConfiguredHosts(path)
	require.NoError(t, err)
	require.Empty(t, hosts)
	require.Equal(t, "/explicit/config", SSHConfigPath("/explicit/config"))
}
