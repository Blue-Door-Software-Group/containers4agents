# draw.io static web app (Apache-2.0), for drawio-tool render / from-mermaid /
# from-vsdx. SHA-256 of draw.war from
# https://github.com/jgraph/drawio/releases/tag/v32.6.10, recorded 2026-11-12.
FROM python:3.12-slim-bookworm AS drawio
ARG DRAWIO_VERSION=32.6.10
ARG DRAWIO_SHA256=a57b06b3b6a876a351dacfe65d027d9509319cd030e9bacf6ae04a034d66a38f
ADD --checksum=sha256:${DRAWIO_SHA256} \
    https://github.com/jgraph/drawio/releases/download/v${DRAWIO_VERSION}/draw.war /tmp/draw.war
COPY images/runtime/DRAWIO-LICENSE /tmp/DRAWIO-LICENSE
# Keep only the browser app: no Java (WEB-INF/META-INF), source maps, the
# unminified sources or the integration bundle, none of which index.html loads.
RUN apt-get update && apt-get install -y --no-install-recommends unzip && \
    mkdir /opt/drawio && cd /opt/drawio && \
    unzip -q /tmp/draw.war -x 'WEB-INF/*' 'META-INF/*' '*.map' 'js/diagramly/*' 'js/integrate.min.js' && \
    cp /tmp/DRAWIO-LICENSE LICENSE && echo "${DRAWIO_VERSION}" > VERSION && \
    chmod -R a+rX /opt/drawio

FROM python:3.12-slim-bookworm

LABEL org.opencontainers.image.source="https://github.com/Blue-Door-Software-Group/containers4agents"

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    coreutils \
    file \
    findutils \
    gawk \
    graphviz \
    grep \
    jq \
    libmagic1 \
    libreoffice-calc \
    libreoffice-impress \
    libreoffice-writer \
    pandoc \
    p7zip-full \
    poppler-utils \
    procps \
    qpdf \
    sed \
    tar \
    unzip \
    xz-utils \
    zip \
    fonts-dejavu \
    fonts-liberation \
    fonts-noto-core \
    fonts-noto-cjk \
    fonts-noto-color-emoji \
    # The libraries Playwright's headless Chromium shell links against (for
    # drawio-tool); its bundled Node.js driver needs libatomic1.
    libasound2 libatk-bridge2.0-0 libatk1.0-0 libatomic1 libatspi2.0-0 libcairo2 \
    libcups2 libdrm2 libgbm1 libnspr4 libnss3 libpango-1.0-0 libxcomposite1 \
    libxdamage1 libxfixes3 libxi6 libxkbcommon0 libxrandr2 \
    && rm -rf /var/lib/apt/lists/*

COPY images/runtime/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt && rm -f /tmp/requirements.txt

# draw.io helper CLI (see docs/DIAGRAM_TOOLS.md): Graphviz for layout, and
# Playwright's headless Chromium shell running the draw.io web app for render and
# conversion. `--only-shell` rather than `--with-deps`, which would add Xvfb,
# Mesa/LLVM and extra fonts (~230 MB); fonts come from the packages above.
ARG PLAYWRIGHT_VERSION=1.65.1
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
    DRAWIO_WEBAPP=/opt/drawio
RUN pip install --no-cache-dir "playwright==${PLAYWRIGHT_VERSION}" && \
    playwright install --only-shell chromium && \
    rm -rf /opt/ms-playwright/ffmpeg-* && \
    chmod -R a+rX /opt/ms-playwright && \
    ! ldd /opt/ms-playwright/chromium_headless_shell-*/*/chrome-headless-shell | grep "not found"
COPY --from=drawio /opt/drawio /opt/drawio
RUN mkdir -p /usr/share/doc/drawio && ln -s /opt/drawio/LICENSE /usr/share/doc/drawio/LICENSE
COPY images/runtime/drawio-tool/ /usr/local/lib/drawio-tool/
RUN ln -s /usr/local/lib/drawio-tool/drawio-tool /usr/local/bin/drawio-tool

RUN useradd --create-home --uid 10001 --shell /bin/bash c4a && \
    mkdir -p /workspace && chown -R c4a:c4a /workspace /home/c4a

WORKDIR /workspace
USER c4a

CMD ["bash", "-lc", "sleep infinity"]
