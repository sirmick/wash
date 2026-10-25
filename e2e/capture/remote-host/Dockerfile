# The "other machine" for the remote-apps screenshot: an arm64 Debian box
# (run under qemu-user on an amd64 capture host) with sshd and a normal wash
# install, which is all docs/REMOTE.md asks of a remote host. Built and run
# by e2e/capture/screenshots.cap.ts (scene "remote"); the build context is
# this directory plus the arm64 multicall binary copied in as ./wash.
FROM --platform=linux/arm64 debian:13-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server procps ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /run/sshd \
 && useradd -m -s /bin/bash pi \
 && printf 'PasswordAuthentication no\nAllowStreamLocalForwarding yes\nStreamLocalBindUnlink yes\n' \
      > /etc/ssh/sshd_config.d/wash.conf

COPY wash /usr/local/bin/wash
RUN /usr/local/bin/wash install-symlinks /usr/local/bin

# A small lived-in home for Files and the terminal to show.
COPY --chown=pi:pi home/ /home/pi/

COPY entrypoint.sh /usr/local/sbin/pi-init
ENTRYPOINT ["/usr/local/sbin/pi-init"]
