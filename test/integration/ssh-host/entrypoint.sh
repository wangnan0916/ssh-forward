#!/bin/sh

set -eu

install -o testdev -g testdev -m 0600 /run/fixture/authorized_keys /home/testdev/.ssh/authorized_keys
ssh-keygen -A >/dev/null 2>&1

exec /usr/sbin/sshd -D -e \
    -o AllowTcpForwarding=yes \
    -o AllowUsers=testdev \
    -o AuthenticationMethods=publickey \
    -o GatewayPorts=no \
    -o KbdInteractiveAuthentication=no \
    -o PasswordAuthentication=no \
    -o PermitRootLogin=no \
    -o PubkeyAuthentication=yes \
    -o UsePAM=no \
    -o X11Forwarding=no
