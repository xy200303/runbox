# Two runtime images:
#   docker build --target minimal .   Python + Node + poppler, no LibreOffice
#   docker build .                    default: adds LibreOffice and data libraries
#
# The container intentionally runs as root. It needs to hand each sandbox
# identity a dedicated UID and chown that identity's workspace. The compose
# file drops every capability except the ownership ones and KILL (which lets a
# timeout terminate a command running as another UID). Both runtime images use
# tini as PID 1 so killed command trees are reaped.

ARG GO_VERSION=1.26.5

FROM golang:${GO_VERSION}-alpine AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/runboxd ./cmd/runboxd

# Tiny init used as PID 1 in the runtime images. It forwards signals and reaps
# the orphaned children of a command tree killed on timeout.
FROM debian:bookworm-slim AS tini
ARG APT_MIRROR=deb.debian.org
RUN if [ "${APT_MIRROR}" != "deb.debian.org" ]; then \
      sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources; \
    fi \
 && apt-get update \
 && apt-get install -y --no-install-recommends tini \
 && rm -rf /var/lib/apt/lists/*

FROM debian:bookworm-slim AS runtime
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
      ca-certificates curl git tzdata procps util-linux unzip zip \
      python3 python3-pip python3-venv nodejs npm poppler-utils fonts-dejavu-core \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/runboxd /usr/local/bin/runboxd
RUN mkdir -p /workspace
WORKDIR /workspace
EXPOSE 8099
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8099/ready || exit 1
ENTRYPOINT ["/usr/local/bin/runboxd"]

# Smaller image for text/code workloads that do not need office conversion.
FROM runtime AS minimal
COPY --from=tini /usr/bin/tini /usr/local/bin/tini
ENTRYPOINT ["/usr/local/bin/tini", "--", "/usr/local/bin/runboxd"]

# Default image. Adds LibreOffice, CJK fonts and the common document/data
# libraries used by agent sandboxes.
FROM runtime AS full
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
      pillow python-dateutil chardet tabulate markdown beautifulsoup4 lxml
RUN npm install -g --silent --fetch-retries=5 xlsx csv-parse dayjs
COPY --from=tini /usr/bin/tini /usr/local/bin/tini
ENTRYPOINT ["/usr/local/bin/tini", "--", "/usr/local/bin/runboxd"]
