# Optional, larger sandbox image: the runtime image plus headless Chromium and
# the draw.io web app, so `drawio-tool render|from-mermaid|from-vsdx` work.
# See docs/DIAGRAM_TOOLS.md. Build from the repository root:
#   docker build -f images/runtime-diagrams/Dockerfile \
#     --build-arg RUNTIME_IMAGE=<runtime image> -t <tag> .
ARG RUNTIME_IMAGE=ghcr.io/blue-door-software-group/containers4agents-runtime:v1

FROM ${RUNTIME_IMAGE} AS drawio
USER root
# draw.io static web app (Apache-2.0). SHA-256 of draw.war from
# https://github.com/jgraph/drawio/releases/tag/v32.6.10, recorded 2026-11-12.
ARG DRAWIO_VERSION=32.6.10
ARG DRAWIO_SHA256=a57b06b3b6a876a351dacfe65d027d9509319cd030e9bacf6ae04a034d66a38f
ADD --checksum=sha256:${DRAWIO_SHA256} \
    https://github.com/jgraph/drawio/releases/download/v${DRAWIO_VERSION}/draw.war /tmp/draw.war
COPY images/runtime-diagrams/DRAWIO-LICENSE /tmp/DRAWIO-LICENSE
# Keep only the browser app: no Java (WEB-INF/META-INF), source maps, the
# unminified sources or the integration bundle, none of which index.html loads.
RUN mkdir /opt/drawio && cd /opt/drawio && \
    unzip -q /tmp/draw.war -x 'WEB-INF/*' 'META-INF/*' '*.map' 'js/diagramly/*' 'js/integrate.min.js' && \
    cp /tmp/DRAWIO-LICENSE LICENSE && echo "${DRAWIO_VERSION}" > VERSION && \
    chmod -R a+rX /opt/drawio

FROM ${RUNTIME_IMAGE}
USER root
ARG PLAYWRIGHT_VERSION=1.65.1
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    DRAWIO_WEBAPP=/opt/drawio
# Playwright (Apache-2.0) with its headless Chromium shell. Instead of
# `playwright install --with-deps` (which adds Xvfb, Mesa/LLVM and extra fonts,
# ~230 MB) install only the libraries the headless shell links against; the
# bundled Node.js driver needs libatomic1. Fonts come from the runtime image.
RUN apt-get update && apt-get install -y --no-install-recommends \
    libasound2 libatk-bridge2.0-0 libatk1.0-0 libatomic1 libatspi2.0-0 libcairo2 \
    libcups2 libdrm2 libgbm1 libnspr4 libnss3 libpango-1.0-0 libxcomposite1 \
    libxdamage1 libxfixes3 libxi6 libxkbcommon0 libxrandr2 && \
    rm -rf /var/lib/apt/lists/* && \
    pip install --no-cache-dir "playwright==${PLAYWRIGHT_VERSION}" && \
    playwright install --only-shell chromium && \
    rm -rf /opt/ms-playwright/ffmpeg-* && \
    chmod -R a+rX /opt/ms-playwright && \
    ! ldd /opt/ms-playwright/chromium_headless_shell-*/*/chrome-headless-shell | grep "not found"
COPY --from=drawio /opt/drawio /opt/drawio
RUN mkdir -p /usr/share/doc/drawio && ln -s /opt/drawio/LICENSE /usr/share/doc/drawio/LICENSE

USER c4a
