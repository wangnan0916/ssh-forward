package openssh

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScannerShellHelpers(t *testing.T) {
	helpers := scannerHelperSource(t)
	script := helpers + `
set -eu
has_ssh_probe=0

looks_like_ssh_banner "SSH-2.0-OpenSSH_9.0"
looks_like_ssh_banner "SSH-1.99-compat"
if looks_like_ssh_banner "HTTP/1.1 200 OK"; then
    echo "http treated as ssh" >&2
    exit 1
fi
if looks_like_ssh_banner ""; then
    echo "empty treated as ssh" >&2
    exit 1
fi
`
	command := exec.Command("sh", "-s")
	command.Stdin = strings.NewReader(script)
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "helper script failed: %s", output)
}

func scannerHelperSource(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	script, err := os.ReadFile(filepath.Join(filepath.Dir(file), "scanner.sh"))
	require.NoError(t, err)
	begin := []byte("# --- scanner helpers begin ---\n")
	end := []byte("# --- scanner helpers end ---\n")
	start := bytes.Index(script, begin)
	stop := bytes.Index(script, end)
	require.False(t, start < 0 || stop < 0 || stop <= start, "scanner helper markers missing")
	return string(script[start+len(begin) : stop])
}
