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

FROM eclipse-temurin:17-jre-jammy

ARG CROMWELL_VERSION=92
ARG CROMWELL_SHA256=e0e3a050d4124e81369a79059e5774142b2f06bd89df4a0b035f559db85cedf5
ARG WOMTOOL_SHA256=99cd3675c48696470f4d4e8b397fc613d7b342eb2ef2fa96f86db114bd9ed5f8

RUN apt-get update \
    && apt-get install -y --no-install-recommends bash ca-certificates curl git \
    && rm -rf /var/lib/apt/lists/*

RUN mkdir -p /opt/cromwell \
    && curl -fsSL -o /opt/cromwell/cromwell.jar "https://github.com/broadinstitute/cromwell/releases/download/${CROMWELL_VERSION}/cromwell-${CROMWELL_VERSION}.jar" \
    && echo "${CROMWELL_SHA256}  /opt/cromwell/cromwell.jar" | sha256sum -c - \
    && curl -fsSL -o /opt/cromwell/womtool.jar "https://github.com/broadinstitute/cromwell/releases/download/${CROMWELL_VERSION}/womtool-${CROMWELL_VERSION}.jar" \
    && echo "${WOMTOOL_SHA256}  /opt/cromwell/womtool.jar" | sha256sum -c -

RUN useradd -u 1000 -m conquest \
    && mkdir -p /workspace /tmp \
    && chown -R 1000:1000 /workspace /tmp /opt/cromwell /home/conquest

COPY --from=go-builder /conquest /usr/local/bin/conquest
RUN chmod 755 /usr/local/bin/conquest

ENV JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=/tmp

USER 1000:1000
WORKDIR /workspace

ENTRYPOINT ["/usr/local/bin/conquest"]
