# Base image of homerun2-ai-pitcher's `-claude` variant (#13): Node and the
# Claude Code CLI, which MODE=cluster-analysis runs as `claude -p`. ko puts the
# ai-pitcher binary on top (KO_DEFAULTBASEIMAGE); the Go service itself still
# has no Dockerfile -- this is infrastructure, like a distroless base.
#
# renovate: datasource=docker depName=node
FROM node:24-bookworm-slim

# renovate: datasource=npm depName=@anthropic-ai/claude-code
ARG CLAUDE_CODE_VERSION=2.1.323

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && npm install -g --omit=dev "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
 && npm cache clean --force \
 && claude --version

# No self-update in a pinned image, no telemetry from a job. HOME must be
# writable: the CronJob mounts an emptyDir at /tmp (read-only root).
ENV DISABLE_AUTOUPDATER=1 \
    DISABLE_TELEMETRY=1 \
    DISABLE_ERROR_REPORTING=1 \
    HOME=/tmp

# The node image's unprivileged user.
USER 1000:1000
