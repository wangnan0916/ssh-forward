FROM ubuntu:24.04@sha256:561618e2c15bf2397621dd04f96926663a3b5616c189cf7e38db7e82f5c538ea

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates \
        iproute2 \
        lsof \
        openssh-server \
        procps \
        python3 \
    && rm -rf /var/lib/apt/lists/* \
    && rm -f /etc/ssh/ssh_host_* \
    && useradd --create-home --shell /bin/sh testdev \
    && passwd -d testdev \
    && install -d -m 0755 /run/sshd \
    && install -d -o testdev -g testdev -m 0700 /home/testdev/.ssh

COPY --chmod=0755 entrypoint.sh /usr/local/bin/ssh-forward-test-host
COPY --chmod=0755 fixture.py /usr/local/lib/ssh-forward-test/fixture.py

EXPOSE 22

ENTRYPOINT ["/usr/local/bin/ssh-forward-test-host"]
