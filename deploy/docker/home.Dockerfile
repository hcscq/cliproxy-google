# Build context: repository root.  docker build -f deploy/docker/home.Dockerfile -t cliproxy-google-home .
# home/go.mod replaces the proxy module with ../proxy, so both directories are copied.
# Management web panel (web/, Vite + React + TypeScript), built here and embedded
# into the Home binary via go:embed.
FROM node:20-bookworm-slim AS panel
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build && test -s dist/management.html

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
COPY --from=panel /web/dist/ ./home/internal/managementasset/static/
RUN test -s home/internal/managementasset/static/management.html
RUN cd home && CGO_ENABLED=1 GOOS=linux go build -buildvcs=false \
    -ldflags="-s -w -X 'main.Version=${VERSION}' -X 'main.Commit=${COMMIT}' -X 'main.BuildDate=${BUILD_DATE}'" \
    -o /out/CLIProxyAPIHome ./cmd/home/ \
    && CGO_ENABLED=0 go build -buildvcs=false -ldflags="-s -w" -o /out/egress-agent ./cmd/egress-agent/

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends tzdata ca-certificates && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 10001 cliproxy && useradd -u 10001 -g 10001 -d /home/cliproxy -m cliproxy
COPY --from=builder /out/CLIProxyAPIHome /app/CLIProxyAPIHome
COPY --from=builder /out/egress-agent /app/egress-agent
COPY home/config.example.yaml /app/config.example.yaml
COPY home/LICENSE NOTICE /app/
WORKDIR /app
USER 10001:10001
ENV TZ=Asia/Shanghai
EXPOSE 8327
ENTRYPOINT ["/app/CLIProxyAPIHome"]
