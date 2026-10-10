# Build context: repository root.  docker build -f deploy/docker/home.Dockerfile -t cliproxy-google-home .
# home/go.mod replaces the proxy module with ../proxy, so both directories are copied.
# Management web panel: the panel source repository (router-for-me/Home-Management-Center)
# is private, so the compiled panel is vendored from a pinned, checksum-verified
# CLIProxyAPIHome release (MIT) by extracting the files embedded in its binary.
FROM golang:1.26-bookworm AS panel
ARG PANEL_RELEASE=v1.1.0
ARG PANEL_ASSET=CLIProxyAPIHome_1.1.0_linux_amd64.tar.gz
ARG PANEL_SHA256=afc33216b20a86e69f43e9318e0fba4b6619fd37746d8f3a228f6f55db6f5e83
WORKDIR /panel
COPY tools/extract-embed/ ./extract-embed/
RUN curl -fsSL -o home.tgz "https://github.com/router-for-me/CLIProxyAPIHome/releases/download/${PANEL_RELEASE}/${PANEL_ASSET}" \
    && echo "${PANEL_SHA256}  home.tgz" | sha256sum -c - \
    && mkdir bin && tar xzf home.tgz -C bin \
    && cd extract-embed && go build -o /panel/xe . \
    && /panel/xe -bin /panel/bin/cli-proxy-api -prefix static/ -out /panel/static -require management.html

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
COPY --from=panel /panel/static/ ./home/internal/managementasset/static/
RUN test -s home/internal/managementasset/static/management.html
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
