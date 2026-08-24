#!/bin/sh

set -eu

fixture_port_v4=${SSH_FORWARD_FIXTURE_PORT_V4:-38080}
fixture_port_v6=${SSH_FORWARD_FIXTURE_PORT_V6:-38081}
fixture_port_dual_stack=${SSH_FORWARD_FIXTURE_PORT_DUAL_STACK:-38082}
fixture_user=${SSH_FORWARD_TEST_USER:-testdev}

install -o "$fixture_user" -g "$fixture_user" -m 0600 /run/fixture/authorized_keys "/home/$fixture_user/.ssh/authorized_keys"
ssh-keygen -A >/dev/null 2>&1

fixture_v4_pid=''
fixture_v6_pid=''
fixture_dual_stack_pid=''
sshd_pid=''
cleanup() {
    trap - HUP INT TERM
    for pid in "$sshd_pid" "$fixture_v4_pid" "$fixture_v6_pid" "$fixture_dual_stack_pid"; do
        [ -z "$pid" ] || kill -TERM "$pid" 2>/dev/null || true
    done
    for pid in "$sshd_pid" "$fixture_v4_pid" "$fixture_v6_pid" "$fixture_dual_stack_pid"; do
        [ -z "$pid" ] || wait "$pid" 2>/dev/null || true
    done
}
trap 'cleanup; exit 143' HUP INT TERM

runuser -u "$fixture_user" -- /usr/bin/socat \
    "TCP4-LISTEN:$fixture_port_v4,bind=127.0.0.1,reuseaddr,fork" EXEC:/bin/cat &
fixture_v4_pid=$!
runuser -u "$fixture_user" -- /usr/bin/socat \
    "TCP6-LISTEN:$fixture_port_v6,bind=[::1],ipv6only=1,reuseaddr,fork" EXEC:/bin/cat &
fixture_v6_pid=$!
runuser -u "$fixture_user" -- /usr/bin/socat \
    "TCP6-LISTEN:$fixture_port_dual_stack,bind=[::],ipv6only=0,reuseaddr,fork" EXEC:/bin/cat &
fixture_dual_stack_pid=$!
ready=0
for _ in $(seq 1 100); do
    sockets=$(ss -H -ltn)
    if printf '%s\n' "$sockets" | grep -q "127.0.0.1:$fixture_port_v4" \
        && printf '%s\n' "$sockets" | grep -q "\[::1\]:$fixture_port_v6" \
        && printf '%s\n' "$sockets" | grep -q "\*:$fixture_port_dual_stack"; then
        ready=1
        break
    fi
    kill -0 "$fixture_v4_pid" "$fixture_v6_pid" "$fixture_dual_stack_pid" 2>/dev/null || break
    sleep 0.02
done
if [ "$ready" -ne 1 ]; then
    printf 'integration response fixture did not become ready\n' >&2
    cleanup
    exit 1
fi

/usr/sbin/sshd -D -e \
    -o AllowTcpForwarding=yes \
    -o AllowUsers="$fixture_user" \
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
