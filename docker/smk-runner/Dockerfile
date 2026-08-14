FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /conquest ./cmd/conquest/

FROM python:3.12-slim

ARG SNAKEMAKE_VERSION=9.17.3

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash bzip2 ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*

# Miniforge ships per-architecture installers, so resolve the asset from the
# actual build platform instead of hardcoding x86_64 -- that made arm64 builds
# download the amd64 installer and fail.
RUN set -eux; \
    case "$(uname -m)" in \
        x86_64)  MINIFORGE_ARCH=x86_64 ;; \
        aarch64|arm64) MINIFORGE_ARCH=aarch64 ;; \
        *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/conda-forge/miniforge/releases/latest/download/Miniforge3-Linux-${MINIFORGE_ARCH}.sh" -o /tmp/miniforge.sh; \
    bash /tmp/miniforge.sh -b -p /opt/conda; \
    rm /tmp/miniforge.sh

ENV PATH=/opt/conda/bin:$PATH

WORKDIR /opt/conquest-python
COPY python/ /opt/conquest-python/
RUN pip install --no-cache-dir --upgrade pip \
    && pip install --no-cache-dir "snakemake==${SNAKEMAKE_VERSION}" "pandas>=2,<3" \
    && pip install --no-cache-dir /opt/conquest-python

RUN useradd -u 1000 -m conquest \
    && mkdir -p /workspace /tmp \
    && chown -R 1000:1000 /workspace /tmp /opt/conda

COPY --from=go-builder /conquest /usr/local/bin/conquest
RUN chmod 755 /usr/local/bin/conquest

USER 1000:1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/conquest"]
