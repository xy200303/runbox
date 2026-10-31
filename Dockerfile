# Runtime images, in increasing size:
#   minimal  Debian 12 slim + agent CLI toolbox + Python/Node (no LibreOffice)
#   toolbox  minimal + build-essential and development headers
#   full     toolbox + LibreOffice, CJK fonts and common data libraries
#
# Debian slim keeps the base small and apt predictable, while still giving an
# agent the usual Debian/Ubuntu layout: bash, apt, /usr/bin, Python, Node and
# the standard GNU tools.
#
# The container intentionally runs as root. It needs to hand each sandbox
# identity a dedicated UID and chown that identity's workspace. The compose
# file drops every capability except the ownership ones and KILL (which lets a
# timeout terminate a command running as another UID). All images use tini as
# PID 1 so killed command trees are reaped.

ARG GO_VERSION=1.26.10
ARG DEBIAN_VERSION=bookworm-slim

FROM golang:${GO_VERSION}-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/runboxd ./cmd/runboxd

# Tiny init used as PID 1. It forwards signals and reaps the orphaned children
# of a command tree killed on timeout.
FROM debian:${DEBIAN_VERSION} AS tini
ARG APT_MIRROR=deb.debian.org
RUN if [ "${APT_MIRROR}" != "deb.debian.org" ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources; \
    fi \
 && apt-get update \
 && apt-get install -y --no-install-recommends tini \
 && rm -rf /var/lib/apt/lists/*

FROM debian:${DEBIAN_VERSION} AS runtime
ARG APT_MIRROR=deb.debian.org
ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    RUNBOX_ROOT=/workspace \
    RUNBOX_ADDR=:8099
RUN if [ "${APT_MIRROR}" != "deb.debian.org" ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources; \
    fi \
 && apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates curl wget git openssh-client jq ripgrep fd-find bat tree file less nano \
      sqlite3 unzip zip xz-utils tar gzip procps psmisc util-linux iproute2 lsof tzdata \
      python3 python3-pip python3-venv nodejs npm poppler-utils fonts-dejavu-core \
 && ln -s /usr/bin/fdfind /usr/local/bin/fd \
 && ln -s /usr/bin/batcat /usr/local/bin/bat \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/runboxd /usr/local/bin/runboxd
RUN mkdir -p /workspace
WORKDIR /workspace
EXPOSE 8099
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8099/ready || exit 1
ENTRYPOINT ["/usr/local/bin/runboxd"]

# Smallest useful image: code, text, data and document extraction without
# LibreOffice or the heavier Python data stack.
FROM runtime AS minimal
COPY --from=tini /usr/bin/tini /usr/local/bin/tini
ENTRYPOINT ["/usr/local/bin/tini", "--", "/usr/local/bin/runboxd"]

# Compiler and development headers for packages that build native extensions.
FROM minimal AS toolbox
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      build-essential python3-dev libpq-dev libxml2-dev libxslt1-dev \
      libssl-dev libffi-dev zlib1g-dev libjpeg-dev libmagic-dev \
 && rm -rf /var/lib/apt/lists/*

# Default image: everything in toolbox, plus LibreOffice, CJK fonts and common
# document/data libraries used by agent sandboxes.
FROM toolbox AS full
ARG PIP_INDEX=https://pypi.org/simple
ARG NPM_REGISTRY=https://registry.npmjs.org
ENV PIP_INDEX_URL=${PIP_INDEX} NPM_CONFIG_REGISTRY=${NPM_REGISTRY}
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      libreoffice-writer libreoffice-calc libreoffice-impress libreoffice-core \
      fonts-noto-cjk fonts-liberation \
 && rm -rf /var/lib/apt/lists/*
RUN pip3 install --no-cache-dir --break-system-packages --timeout 120 --retries 5 \
      openpyxl xlsxwriter pandas numpy python-docx python-pptx pypdf pdfplumber \
      pillow python-dateutil chardet tabulate markdown beautifulsoup4 lxml \
 && npm install -g --silent --fetch-retries=5 xlsx csv-parse dayjs \
 && npm cache clean --force

