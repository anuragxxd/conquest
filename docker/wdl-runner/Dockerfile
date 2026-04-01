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

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*

# Install miniwdl and the Conquest TES backend plugin.
COPY python/miniwdl_conquest_tes /opt/miniwdl_conquest_tes
RUN pip install --no-cache-dir \
    miniwdl==1.12.1 \
    /opt/miniwdl_conquest_tes

COPY --from=go-builder /conquest /usr/local/bin/conquest
RUN chmod 755 /usr/local/bin/conquest

RUN useradd -u 1000 -m conquest \
    && mkdir -p /workspace /tmp \
    && chown -R 1000:1000 /workspace /tmp /home/conquest

USER 1000:1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/conquest"]
