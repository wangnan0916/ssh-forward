package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

// Run the actual edit APIs in separate processes, not just goroutines sharing
// a mutex. Each worker updates every family of intent in the same config file.
func TestConfigEditsAcrossProcesses(t *testing.T) {
	const workers, edits = 4, 6
	if path := os.Getenv("SSH_FORWARD_CONFIG_EDIT_PROBE"); path != "" {
		worker, err := strconv.Atoi(os.Getenv("SSH_FORWARD_CONFIG_EDIT_WORKER"))
		require.NoError(t, err)
		fmt.Println("ready")
		_, err = io.Copy(io.Discard, os.Stdin)
		require.NoError(t, err)
		for index := range edits {
			id := worker*edits + index
			_, err := EditRememberedForward(path, "dev", &core.RememberedForward{RemotePort: uint16(3000 + id)}, true)
			require.NoError(t, err)
			_, err = EditPublishedForward(path, "dev", &core.PublishedForward{LocalPort: uint16(9000 + id)}, true)
			require.NoError(t, err)
			_, err = EditWorkingDirectoryRule(path, "dev", fmt.Sprintf("/workspace/project%d/**", id), true)
			require.NoError(t, err)
			_, err = EditIgnoredApp(path, fmt.Sprintf("app%d", id), true)
			require.NoError(t, err)
			name := fmt.Sprintf("host%d", id)
			require.NoError(t, EditHost(path, name, &HostTarget{Target: name}, true))
		}
		return
	}

	path := filepath.Join(t.TempDir(), "new", "config.jsonc")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	type child struct {
		cmd    *exec.Cmd
		input  io.WriteCloser
		output *bufio.Reader
		stderr *bytes.Buffer
	}
	children := make([]child, 0, workers)
	for worker := range workers {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfigEditsAcrossProcesses$")
		cmd.Env = append(os.Environ(), "SSH_FORWARD_CONFIG_EDIT_PROBE="+path, "SSH_FORWARD_CONFIG_EDIT_WORKER="+strconv.Itoa(worker))
		input, err := cmd.StdinPipe()
		require.NoError(t, err)
		output, err := cmd.StdoutPipe()
		require.NoError(t, err)
		stderr := &bytes.Buffer{}
		cmd.Stderr = stderr
		require.NoError(t, cmd.Start())
		t.Cleanup(func() {
			_ = input.Close()
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		reader := bufio.NewReader(output)
		line, err := reader.ReadString('\n')
		require.NoError(t, err)
		require.Equal(t, "ready\n", line)
		children = append(children, child{cmd, input, reader, stderr})
	}
	for _, child := range children {
		require.NoError(t, child.input.Close())
	}
	for _, child := range children {
		output, err := io.ReadAll(child.output)
		require.NoError(t, err)
		require.NoError(t, child.cmd.Wait(), "%s\n%s", output, child.stderr.String())
	}

	config, err := LoadConfig(path)
	require.NoError(t, err)
	for worker := range workers {
		for index := range edits {
			id := worker*edits + index
			require.Contains(t, config.scope("dev").Forwards, core.RememberedForward{RemotePort: uint16(3000 + id)}.WithDefaults())
			require.Contains(t, config.scope("dev").Published, core.PublishedForward{LocalPort: uint16(9000 + id)}.WithDefaults())
			require.Contains(t, config.scope("dev").Directories, fmt.Sprintf("/workspace/project%d/**", id))
			require.Contains(t, config.scope("").IgnoredApps, fmt.Sprintf("app%d", id))
			name := fmt.Sprintf("host%d", id)
			require.Equal(t, HostTarget{Target: name}, config.Hosts[name])
			require.Contains(t, config.IgnoredHosts, name)
		}
	}
	info, err := os.Stat(path + ".lock")
	require.NoError(t, err)
	require.EqualValues(t, 0o600, info.Mode().Perm())
}

func TestConfigEditReleasesLockWithoutSaving(t *testing.T) {
	for _, editErr := range []error{nil, errors.New("rejected edit")} {
		t.Run(fmt.Sprint(editErr), func(t *testing.T) {
			path := writeConfigFile(t, `{"schema_version":6,"hosts":{"dev":{"target":"dev"}}}`)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			changed, err := editConfig(path, func(config *configuration) (bool, error) {
				config.IgnoredHosts = append(config.IgnoredHosts, "dev")
				return editErr != nil, editErr
			})
			require.False(t, changed)
			require.ErrorIs(t, err, editErr)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = EditWorkingDirectoryRule(path, "dev", "/workspace/**", true)
			require.NoError(t, err, "lock must be released on every return path")
		})
	}
}
