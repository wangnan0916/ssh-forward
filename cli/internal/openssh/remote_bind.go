package openssh

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/wangnan0916/ssh-forward/cli/internal/core"
)

const remoteBindProbeScript = `set -eu
if [ ! -r /proc/net/tcp ]; then
    printf 'unavailable\n'
    exit 0
fi

port_hex=$(printf '%04X' "$1")
files=/proc/net/tcp
if [ -r /proc/net/tcp6 ]; then
    files="$files /proc/net/tcp6"
fi

awk -v port="$port_hex" '
    NR > 1 && $4 == "0A" {
        split($2, local, ":")
        if (toupper(local[2]) != port) next
        address = toupper(local[1])
        if (FILENAME == "/proc/net/tcp" && address == "0100007F") {
            loopback = 1
        } else if (FILENAME == "/proc/net/tcp6" && address == "0000000000000000FFFF00000100007F") {
            loopback = 1
        } else {
            unsafe = 1
        }
    }
    END {
        if (unsafe) {
            print "unsafe"
        } else if (loopback) {
            print "loopback"
        } else {
            print "missing"
        }
    }
' $files
`

// sshd may override a requested loopback bind when GatewayPorts is enabled.
// Inspect the actual remote listener and fail closed before reporting readiness.
func (a *Adapter) verifyRemoteLoopbackForward(
	ctx context.Context,
	host core.HostAlias,
	master *sshMaster,
	port uint16,
) error {
	probeCtx, cancel := context.WithTimeout(ctx, a.readyTimeout)
	defer cancel()
	arguments := append(
		a.masterClientArguments(host),
		"-T", "-o", "ControlMaster=no",
		string(host), "sh", "-s", "--", strconv.Itoa(int(port)),
	)
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
	switch strings.TrimSpace(stdout.String()) {
	case "loopback":
		return nil
	case "unsafe":
		return backendError("remote_bind_not_loopback")
	default:
		return backendError("remote_bind_unverified")
	}
}
