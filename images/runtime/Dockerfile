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
    && rm -rf /var/lib/apt/lists/*

COPY images/runtime/requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir -r /tmp/requirements.txt && rm -f /tmp/requirements.txt

RUN useradd --create-home --uid 10001 --shell /bin/bash c4a && \
    mkdir -p /workspace && chown -R c4a:c4a /workspace /home/c4a

WORKDIR /workspace
USER c4a

CMD ["bash", "-lc", "sleep infinity"]
