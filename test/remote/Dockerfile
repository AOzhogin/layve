# A throwaway "remote machine" for testing ssh:// Docker contexts:
# Debian with sshd and its own Docker daemon. Started by up.sh.
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server docker.io iptables ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd /root/.ssh
COPY authorized_keys /root/.ssh/authorized_keys
COPY entrypoint.sh /entrypoint.sh
RUN chmod 600 /root/.ssh/authorized_keys && chmod +x /entrypoint.sh \
 && echo 'PermitRootLogin prohibit-password' >> /etc/ssh/sshd_config
ENTRYPOINT ["/entrypoint.sh"]
