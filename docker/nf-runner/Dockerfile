# Conquest Nextflow runner image.
#
# This image layers the current conquest binary onto a curated Nextflow runtime
# base image that already contains the pinned Nextflow binary and platform
# plugins for a supported Nextflow version.

# ---- Build conquest binary ----
ARG NXF_BASE_IMAGE=harbor.pacificanalytics.com/pacificanalytics/conquest-runner:cleanup-20260328011600-nf-24.10.4

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

FROM ${NXF_BASE_IMAGE}

USER root

# Copy conquest binary.
COPY --from=go-builder /conquest /usr/local/bin/conquest
RUN chmod 755 /usr/local/bin/conquest

# Curated plugin allowlist. The runner pod can override this at runtime.
ARG NXF_PLUGINS_ALLOWED="nf-ga4gh,nf-amazon,nf-schema,nf-core-utils,nf-fgbio,nf-prov"
ENV NXF_PLUGINS_ALLOWED=${NXF_PLUGINS_ALLOWED}
ENV NXF_ANSI_LOG=false

USER 1000:1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/conquest"]
