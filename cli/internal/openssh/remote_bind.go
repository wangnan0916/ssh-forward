package openssh

import (
	"context"
	"io"
	"strconv"
	"strings"
)

const remoteBindProbeScript = `set -eu
if [ ! -r /proc/net/tcp ]; then
    printf 'unavailable\n'
    exit 0
fi
port=$(printf '%04X' "$1")
files=/proc/net/tcp
[ -r /proc/net/tcp6 ] && files="$files /proc/net/tcp6"
# shellcheck disable=SC2086
awk -v port="$port" '$4 == "0A" {
    split($2, local, ":")
    if (toupper(local[2]) == port) print toupper(local[1])
}' $files
`

// sshd may override a requested loopback bind when GatewayPorts is enabled.
// Inspect the actual remote listener and fail closed before reporting readiness.
func (a *Adapter) verifyRemoteLoopbackForward(ctx context.Context, master *sshMaster, port uint16) error {
	probeCtx, cancel := context.WithTimeout(ctx, a.readyTimeout)
	defer cancel()
	arguments := append(a.masterClientArguments(), "-T", "-o", "ControlMaster=no", a.target, "sh", "-s", "--", strconv.Itoa(int(port)))
	command := a.commandContext(probeCtx, arguments...)
	stdout := &boundedBuffer{limit: 256}
	command.Stdin = strings.NewReader(remoteBindProbeScript)
	command.Stdout = stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if probeCtx.Err() != nil {
			return backendError("remote_bind_unverified")
		}
		select {
		case <-master.done:
			return master.failure()
		default:
		}
		return backendError("remote_bind_unverified")
	}
	return classifyRemoteBind(stdout.String())
}

func classifyRemoteBind(output string) error {
	text := strings.TrimSpace(output)
	if text == "" || text == "unavailable" {
		return backendError("remote_bind_unverified")
	}
	for line := range strings.SplitSeq(text, "\n") {
		address := strings.ToUpper(strings.TrimSpace(line))
		switch address {
		case procV4Loopback, procV4Mapped:
		default:
			if len(address) != len(procV4Loopback) && len(address) != len(procV6Wildcard) {
				return backendError("remote_bind_unverified")
			}
			return backendError("remote_bind_not_loopback")
		}
	}
	return nil
}
