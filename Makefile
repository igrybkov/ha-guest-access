BINARY      := guest-access
PKG         := ./cmd/guest-access
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE       ?= ghcr.io/igrybkov/ha-guest-access
PLATFORMS   ?= linux/amd64,linux/arm64
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@2026.2.2
# Optional buildx builder; a multi-platform build needs a docker-container one.
BUILDER     ?=

# Dev mode settings (HA_URL, HA_TOKEN) go in .env, which git ignores.
-include .env
export

.PHONY: build test test-ui vet lint run-dev docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) $(PKG)

test:
	go test -race ./...

# Any node with --test (18+) works; the CI runner's preinstalled node is
# enough. There is no UI yet, so the target passes with no tests.
UI_TESTS := $(wildcard internal/web/ui/*_test.mjs)
test-ui:
	$(if $(UI_TESTS),node --test $(UI_TESTS),@echo "no UI tests yet")

vet:
	go vet ./...

lint:
	go run $(STATICCHECK) ./...
	@# Comments and docs must stand on their own: no pointers to planning
	@# notes outside the repo.
	@! grep -rniE --include='*.go' --include='*.js' --include='*.mjs' --include='*.html' --include='*.css' --include='*.yaml' --include='*.md' \
		'(plan|brief) (section|decision|item)|(brief|plan) §|\b(decision|item) [0-9]+\b|\bthe [a-z]+ wave\b|\bwave [0-9]' \
		--exclude=AGENTS.md --exclude=CLAUDE.md . || (echo "lint: comment refers to notes outside the repo" && false)

run-dev:
	mkdir -p data dev-config
	DATA_DIR=$(CURDIR)/data CONFIG_DIR=$(CURDIR)/dev-config go run $(PKG)

docker:
	docker buildx build $(if $(BUILDER),--builder $(BUILDER)) --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .
