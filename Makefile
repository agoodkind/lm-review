GO_MK_URL   := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK       := .make/go.mk
GO_MK_CACHE := $(HOME)/.cache/go-makefile/go.mk

$(GO_MK):
	@[ -f "$@" ] && exit 0; \
	mkdir -p $(dir $@); \
	if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$@"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$@" "$(GO_MK_CACHE)"; \
	elif [ -f "$(GO_MK_CACHE)" ]; then \
		echo "warning: go.mk fetch failed, using cached version"; \
		cp "$(GO_MK_CACHE)" "$@"; \
	else \
		echo "error: go.mk fetch failed and no cache available"; \
		exit 1; \
	fi

-include $(GO_MK)

.PHONY: update-go-mk
update-go-mk:
	@mkdir -p "$(dir $(GO_MK))"
	@curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$(GO_MK)" && \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$(GO_MK)" "$(GO_MK_CACHE)" && \
		echo "go.mk updated"

BINARY := lm-review
CMD    := ./cmd/$(BINARY)

.DEFAULT_GOAL := check

.PHONY: build deploy clean review-diff review-pr review-repo

build:
	go build $(CMD)
	@command -v lm-review >/dev/null 2>&1 && lm-review diff || true

deploy:
	go install $(CMD)
	@echo "deployed: $$(go env GOPATH)/bin/$(BINARY)"

clean:
	rm -f $(BINARY)

setup-hooks:
	git config core.hooksPath .githooks
	@echo "git hooks configured"

review-diff:
	lm-review diff

review-pr:
	lm-review pr

review-deep:
	lm-review diff --deep &

review-repo:
	lm-review repo --async
