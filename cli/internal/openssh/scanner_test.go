package openssh

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScannerBootstrapPreservesLinesAndRemainingStdin(t *testing.T) {
	command := exec.Command("sh", "-c", scannerBootstrap)
	command.Stdin = strings.NewReader("printf '%s\\n' first\nIFS= read -r line || exit 1\nprintf '%s\\n' \"$line\"\n" + scannerScriptEnd + "\nsecond\n")
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "bootstrap failed: %s", output)
	require.Equal(t, "first\nsecond\n", string(output))
}
