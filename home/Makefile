PANEL_DIR ?= $(CURDIR)/../web
EMBED_STATIC_DIR := internal/managementasset/static
LOCAL_BINARY ?= CLIProxyAPIHome
DOCKER_IMAGE ?= cliproxyapi-home:embedded-local

.PHONY: embed-local panel-assets-local docker-embed-local run-embedded-local clean-embedded-local

# Local-only helper. GitHub Actions builds embedded assets independently.
embed-local: panel-assets-local
	go build -o "$(LOCAL_BINARY)" ./cmd/home

docker-embed-local: panel-assets-local
	docker build -t "$(DOCKER_IMAGE)" .

panel-assets-local:
	@command -v npm >/dev/null || { echo "npm is required"; exit 1; }
	cd "$(PANEL_DIR)" && npm ci && npm run build
	mkdir -p "$(EMBED_STATIC_DIR)"
	touch "$(EMBED_STATIC_DIR)/.gitkeep"
	find "$(EMBED_STATIC_DIR)" -mindepth 1 ! -name ".gitkeep" -exec rm -rf {} +
	cp -R "$(PANEL_DIR)/dist/." "$(EMBED_STATIC_DIR)/"

run-embedded-local: panel-assets-local
	go run ./cmd/home

clean-embedded-local:
	rm -rf "$(EMBED_STATIC_DIR)"
	mkdir -p "$(EMBED_STATIC_DIR)"
	touch "$(EMBED_STATIC_DIR)/.gitkeep"
