# Snakemake 7.x runner image.
#
# SM7 workflows run all tasks locally inside the runner pod (no TES executor).
# This image bundles Snakemake 7.32.4, Miniforge (conda), and the Conquest
# runner binary. The runner uses --cores N --use-conda --keep-going mode.
#
# Build:
#   docker build -f docker/smk-runner/v7.Dockerfile \
#     --platform linux/amd64 \
#     -t registry.example.com/conquest/conquest-smk-7.32.4-runner:release-20260329 .

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

FROM python:3.11-slim

# Pin to SM7 — the last release before the v8 breaking changes.
ARG SNAKEMAKE_VERSION=7.32.4

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       bash bzip2 ca-certificates curl git procps build-essential \
    && rm -rf /var/lib/apt/lists/*

# Install Miniforge (conda) — needed for --use-conda.
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

# Install the SM7 Python stack with the system Python 3.11 runtime.
# pandas<2 does not have wheels for newer Python versions pulled in by Miniforge.
RUN /usr/local/bin/python -m pip install --no-cache-dir --upgrade pip \
    && /usr/local/bin/python -m pip install --no-cache-dir \
       "snakemake==${SNAKEMAKE_VERSION}" \
       "numpy<2" \
       "pandas>=1.5,<2" \
       "pulp>=2.7,<2.8"

# No executor plugin for SM7 — tasks run locally via --cores N.

RUN useradd -u 1000 -m conquest \
    && mkdir -p /workspace /tmp \
    && chown -R 1000:1000 /workspace /tmp /opt/conda

COPY --from=go-builder /conquest /usr/local/bin/conquest
RUN chmod 755 /usr/local/bin/conquest

USER 1000:1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/conquest"]
