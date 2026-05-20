# wash — top-level build
#
# Two stages, wired together:
#   1. web — Vite library builds; brotli precompress (if installed);
#            output copied into cmd/<bin>/assets/ for //go:embed.
#   2. go  — CGO_ENABLED=0 go build -trimpath -ldflags="-s -w".
#
# `make verify` enforces static-ELF output. If the web stage is
# skipped, the go stage's //go:embed pattern errors and the build
# fails — a stale or unbuilt frontend cannot silently ship.

GOOS    ?= linux
GOARCH  ?= amd64
GOFLAGS := -trimpath -ldflags=-s\ -w -tags netgo,osusergo

OUT     := out
BINS    := wash-router wash-session wash-about
TARGETS := $(addprefix $(OUT)/,$(BINS))

GO_ENV  := CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH)

PNPM    := pnpm

# Per-binary embed stamps. Each binary's go build depends on its stamp
# so the web stage runs first and updates assets/ if anything changed.
ROUTER_ASSETS  := cmd/wash-router/assets
ROUTER_STAMP   := $(ROUTER_ASSETS)/.stamp

SESSION_ASSETS := cmd/wash-session/assets
SESSION_STAMP  := $(SESSION_ASSETS)/.stamp

.PHONY: all
all: $(TARGETS)

$(OUT):
	mkdir -p $(OUT)

# ----- web stage -----

# pnpm install once; subsequent runs are fast no-ops.
.PHONY: web-deps
web-deps:
	@cd web && $(PNPM) install --silent

.PHONY: web-shell
web-shell: web-deps
	@cd web && $(PNPM) --filter @wash/shell run build

.PHONY: web-session
web-session: web-deps
	@cd web && $(PNPM) --filter @wash/app-session run build

# embed-into-cmd helper. Usage: $(call embed,<src dist dir>,<dst assets dir>)
define embed_dist
	rm -rf $(2)
	mkdir -p $(2)
	cp -R $(1)/. $(2)/
	@if command -v brotli >/dev/null 2>&1; then \
		find $(2) -type f \( -name '*.js' -o -name '*.css' -o -name '*.html' -o -name '*.svg' -o -name '*.json' \) -exec brotli -k -q 11 -f {} + ; \
	else \
		echo "brotli not installed: skipping precompress under $(2)"; \
	fi
	touch $(2)/.stamp
endef

$(ROUTER_STAMP): web-shell
	$(call embed_dist,web/shell/dist,$(ROUTER_ASSETS))

$(SESSION_STAMP): web-session
	$(call embed_dist,web/apps/session/dist,$(SESSION_ASSETS))

# ----- go stage -----

$(OUT)/wash-router: $(ROUTER_STAMP) | $(OUT)
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/wash-router

$(OUT)/wash-session: $(SESSION_STAMP) | $(OUT)
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/wash-session

$(OUT)/wash-about: | $(OUT)
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/wash-about

# ----- meta -----

.PHONY: linux-arm64
linux-arm64:
	$(MAKE) GOARCH=arm64 all

.PHONY: clean
clean:
	rm -rf $(OUT)
	rm -rf web/*/dist web/apps/*/dist
	rm -rf cmd/*/assets

.PHONY: verify
verify: all
	go vet ./...
	go test ./...
	@for f in $(TARGETS); do \
		file "$$f" | grep -qi 'statically' || { echo "$$f is not statically linked"; exit 1; }; \
	done
	@echo "verify: ok"

.PHONY: dev
dev:
	@echo "dev mode not implemented yet"
	@exit 1
