# Test target for the SSH provider end-to-end suite.
#
# The image never contains a private key. e2e/run.sh generates an ed25519 key
# pair at runtime and copies only the public key into authorized_keys.
FROM ubuntu:24.04

SHELL ["/bin/sh", "-c"]

ARG DEBIAN_FRONTEND=noninteractive
# Optional HTTP(S) proxy for apt. The value is supplied by the caller so this
# file does not hard-code a site-specific proxy.
ARG APT_PROXY=""

RUN set -eu; \
    if [ -n "$APT_PROXY" ]; then \
        printf 'Acquire::http::Proxy "%s";\nAcquire::https::Proxy "%s";\n' \
            "$APT_PROXY" "$APT_PROXY" > /etc/apt/apt.conf.d/99-e2e-proxy; \
    fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        coreutils \
        openssh-server \
        sudo; \
    rm -rf /var/lib/apt/lists/* /etc/apt/apt.conf.d/99-e2e-proxy

# root and the non-root e2e user both authenticate by public key only. e2e has
# passwordless sudo so the provider's `sudo -n` escalation can be exercised.
RUN set -eu; \
    useradd --create-home --home-dir /home/e2e --shell /bin/bash e2e; \
    mkdir -p /home/e2e/.ssh /root/.ssh /run/sshd; \
    chown -R e2e:e2e /home/e2e; \
    chmod 0700 /home/e2e/.ssh /root/.ssh; \
    printf 'e2e ALL=(root) NOPASSWD: ALL\n' > /etc/sudoers.d/90-e2e; \
    chmod 0440 /etc/sudoers.d/90-e2e; \
    visudo -cf /etc/sudoers.d/90-e2e

RUN set -eu; \
    printf '%s\n' \
        'PasswordAuthentication no' \
        'KbdInteractiveAuthentication no' \
        'ChallengeResponseAuthentication no' \
        'PermitEmptyPasswords no' \
        'PubkeyAuthentication yes' \
        'AuthenticationMethods publickey' \
        'PermitRootLogin prohibit-password' \
        'AllowUsers root e2e' \
        'AuthorizedKeysFile .ssh/authorized_keys' \
        'StrictModes yes' \
        'UsePAM yes' \
        'Subsystem sftp /usr/lib/openssh/sftp-server' \
        'LogLevel VERBOSE' \
        > /etc/ssh/sshd_config.d/99-e2e.conf; \
    ssh-keygen -A; \
    /usr/sbin/sshd -t

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D", "-e"]
