# Build context: repository root.  docker build -f deploy/docker/home.Dockerfile -t cliproxy-google-home .
# home/go.mod replaces the proxy module with ../proxy, so both directories are copied.
FROM golang:1.26-bookworm AS builder
WORKDIR /src
RUN apt-get update && apt-get install -y --no-install-recommends build-essential git && rm -rf /var/lib/apt/lists/*
COPY proxy/go.mod proxy/go.sum ./proxy/
COPY home/go.mod home/go.sum ./home/
RUN cd home && go mod download
COPY proxy/ ./proxy/
COPY home/ ./home/
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown
# The management web panel is optional. Populate home/internal/managementasset/static
# (see home/Makefile panel-assets-local) before building to embed it.
RUN test -s home/internal/managementasset/static/management.html || echo "WARN: management panel assets not embedded; Management API still works"
RUN cd home && CGO_ENABLED=1 GOOS=linux go build -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /out/CLIProxyAPIHome ./cmd/home/

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10001 cliproxy && useradd -u 10001 -g 10001 -d /home/cliproxy -m cliproxy
COPY --from=builder /out/CLIProxyAPIHome /app/CLIProxyAPIHome
COPY home/config.example.yaml /app/config.example.yaml
COPY home/LICENSE NOTICE /app/
WORKDIR /app
USER 10001:10001
ENV TZ=Asia/Shanghai
EXPOSE 8327
ENTRYPOINT ["/app/CLIProxyAPIHome"]
