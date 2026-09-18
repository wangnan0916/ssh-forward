package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWatchScreenClearsOnlyInteractiveHuman(t *testing.T) {
	var output bytes.Buffer
	screen := watchScreen{writer: &output}
	require.NoError(t, screen.enter())
	require.NoError(t, screen.clear())
	screen.leave()
	require.Empty(t, output.String())

	screen.active = true
	require.NoError(t, screen.enter())
	require.NoError(t, screen.clear())
	screen.leave()
	require.Equal(t, "\x1b[?25l\x1b[H\x1b[2J\x1b[?25h", output.String())
}
