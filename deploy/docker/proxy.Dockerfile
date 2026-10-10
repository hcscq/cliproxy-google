# Build context: repository root.  docker build -f deploy/docker/proxy.Dockerfile -t cliproxy-google-proxy .
FROM golang:1.26-bookworm AS builder
WORKDIR /src/proxy
RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*
COPY proxy/go.mod proxy/go.sum ./
RUN go mod download
COPY proxy/ ./
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=1 GOOS=linux go build -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /out/CLIProxyAPI ./cmd/server/

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10001 cliproxy && useradd -u 10001 -g 10001 -d /home/cliproxy -m cliproxy
COPY --from=builder /out/CLIProxyAPI /app/CLIProxyAPI
COPY proxy/config.example.yaml /app/config.example.yaml
COPY proxy/LICENSE NOTICE /app/
WORKDIR /app
USER 10001:10001
ENV TZ=Asia/Shanghai WRITABLE_PATH=/tmp/cliproxy
EXPOSE 8317
ENTRYPOINT ["/app/CLIProxyAPI"]
