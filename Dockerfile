# ---- Build stage ----
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -o /conquest ./cmd/conquest/

# ---- Migrate binary ----
FROM --platform=linux/amd64 alpine:3.21 AS migrate-dl
RUN wget -qO /tmp/migrate.tar.gz \
    "https://github.com/golang-migrate/migrate/releases/download/v4.17.0/migrate.linux-amd64.tar.gz" \
    && tar -xzf /tmp/migrate.tar.gz -C /usr/local/bin/ \
    && chmod +x /usr/local/bin/migrate

# ---- Runtime stage ----
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 1000 conquest

COPY --from=builder /conquest /usr/local/bin/conquest
COPY --from=builder /src/migrations /migrations
COPY --from=migrate-dl /usr/local/bin/migrate /usr/local/bin/migrate

USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/conquest"]
