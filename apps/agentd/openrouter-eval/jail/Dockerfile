# The jail: an Alpine container running wash headless, for agents on
# models we do not trust. Nothing of the host is mounted but what a run is
# given (an output folder); wash's config, state, the project and the
# adapters' data live and die here, and it runs as an unprivileged user. Its
# network is not limited: the line it holds is the host's files.
#
# Built by jail/build.sh from the multicall binary (out/wash, static) and,
# for the smoke test, the fake ACP adapter (out/e2e/codex-acp).
FROM alpine:3.21

RUN apk add --no-cache socat jq git curl python3 nodejs npm go \
    && npm install -g opencode-ai@1.18.32 \
    && adduser -D agent

COPY wash /opt/wash/wash
COPY codex-acp /usr/local/bin/codex-acp
RUN /opt/wash/wash install-symlinks /opt/wash >/dev/null

# What OpenCode fetches on first use, fetched now (prewarm.mjs).
COPY prewarm.mjs /opt/jail/prewarm.mjs
USER agent
WORKDIR /home/agent
RUN node /opt/jail/prewarm.mjs

USER root
COPY smoke.sh run.sh audit.py /opt/jail/
COPY shakedown /opt/jail/shakedown
USER agent
ENV PATH=/opt/wash:$PATH WASH_JAIL=1
