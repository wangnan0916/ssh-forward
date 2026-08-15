#!/bin/sh

set -eu

install -o testdev -g testdev -m 0600 /run/fixture/authorized_keys /home/testdev/.ssh/authorized_keys
ssh-keygen -A >/dev/null 2>&1

fixture_pid=''
sshd_pid=''
cleanup() {
    trap - HUP INT TERM
    [ -z "$sshd_pid" ] || kill -TERM "$sshd_pid" 2>/dev/null || true
    [ -z "$fixture_pid" ] || kill -TERM "$fixture_pid" 2>/dev/null || true
    [ -z "$sshd_pid" ] || wait "$sshd_pid" 2>/dev/null || true
    [ -z "$fixture_pid" ] || wait "$fixture_pid" 2>/dev/null || true
}
trap 'cleanup; exit 143' HUP INT TERM

runuser -u testdev -- /usr/bin/python3 /usr/local/lib/ssh-forward-test/fixture.py &
fixture_pid=$!
ready=0
for _ in $(seq 1 100); do
    sockets=$(ss -H -ltn)
    if printf '%s\n' "$sockets" | grep -q '127.0.0.1:38080' \
        && printf '%s\n' "$sockets" | grep -q '\[::1\]:38081'; then
        ready=1
        break
    fi
    kill -0 "$fixture_pid" 2>/dev/null || break
    sleep 0.02
done
if [ "$ready" -ne 1 ]; then
    printf 'integration response fixture did not become ready\n' >&2
    cleanup
    exit 1
fi

/usr/sbin/sshd -D -e \
    -o AllowTcpForwarding=yes \
    -o AllowUsers=testdev \
    -o AuthenticationMethods=publickey \
    -o GatewayPorts=no \
    -o KbdInteractiveAuthentication=no \
    -o PasswordAuthentication=no \
    -o PermitRootLogin=no \
    -o PubkeyAuthentication=yes \
    -o UsePAM=no \
    -o X11Forwarding=no &
sshd_pid=$!
set +e
wait "$sshd_pid"
status=$?
set -e
sshd_pid=''
cleanup
exit "$status"
