# wash — top-level build
#
# Two stages:
#   1. web   — Vite library builds + brotli precompress, copied into cmd/<bin>/assets/
#   2. go    — CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"
#
# Both must run for a release build; `make verify` enforces static-ELF output.

GOOS    ?= linux
GOARCH  ?= amd64
GOFLAGS := -trimpath -ldflags=-s\ -w -tags netgo,osusergo

OUT     := out
BINS    := wash-router wash-session wash-about
TARGETS := $(addprefix $(OUT)/,$(BINS))

GO_ENV  := CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH)

.PHONY: all
all: $(TARGETS)

$(OUT):
	mkdir -p $(OUT)

# Pattern rule: out/wash-X depends on the matching cmd/wash-X/ source tree.
$(OUT)/%: $(OUT) FORCE
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/$*

.PHONY: FORCE
FORCE:

.PHONY: linux-arm64
linux-arm64:
	$(MAKE) GOARCH=arm64 all

.PHONY: clean
clean:
	rm -rf $(OUT)
	rm -rf web/*/dist web/apps/*/dist
	rm -rf cmd/*/assets

.PHONY: verify
verify:
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
