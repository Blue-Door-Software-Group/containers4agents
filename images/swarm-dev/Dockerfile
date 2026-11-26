# swarm-dev: the runtime image (office, PDF and data tools) plus a developer toolchain, for agents
# that build and test code in a long-lived workspace. Registered with `network: egress` so it can
# clone repositories and fetch packages (see scripts/egress-firewall.sh for the deny rules).
ARG BASE_IMAGE=ghcr.io/blue-door-software-group/containers4agents-runtime:v1
FROM ${BASE_IMAGE}

ARG GO_VERSION=1.24.5
ARG NODE_VERSION=22.17.1

USER root
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    curl \
    git \
    openssh-client \
    patch \
    ripgrep \
    && rm -rf /var/lib/apt/lists/*

RUN arch="$(dpkg --print-architecture)" && \
    case "$arch" in amd64) goarch=amd64; nodearch=x64 ;; arm64) goarch=arm64; nodearch=arm64 ;; *) echo "unsupported $arch" >&2; exit 1 ;; esac && \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${goarch}.tar.gz" | tar -C /usr/local -xz && \
    curl -fsSL "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${nodearch}.tar.xz" | tar -C /usr/local --strip-components=1 -xJ && \
    /usr/local/go/bin/go version && node --version && npm --version

# Commands run as whatever user the profile names; HOME and the tool caches live in the
# persistent workspace so clones, module caches and git config survive restarts.
ENV PATH=/usr/local/go/bin:/workspace/.go/bin:$PATH \
    HOME=/workspace/.home \
    GOPATH=/workspace/.go \
    GOCACHE=/workspace/.cache/go-build \
    GOFLAGS=-modcacherw \
    npm_config_cache=/workspace/.cache/npm \
    XDG_CACHE_HOME=/workspace/.cache \
    GOTMPDIR=/workspace/.cache/go-tmp \
    GIT_TERMINAL_PROMPT=0

# Login shells (exec runs `sh -lc`/`bash -lc`) reset PATH from /etc/profile: keep the toolchain on
# it, and create HOME and Go's temp dir in the workspace (/tmp is mounted noexec, so test binaries
# built there could not run).
RUN printf '%s\n' \
    'export PATH=/usr/local/go/bin:/workspace/.go/bin:$PATH' \
    'mkdir -p "$HOME" "$GOTMPDIR" 2>/dev/null || true' \
    > /etc/profile.d/10-swarm-dev.sh

WORKDIR /workspace
USER c4a
CMD ["bash", "-lc", "sleep infinity"]
