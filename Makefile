BINARY := lm-review
CMD    := ./cmd/$(BINARY)
VPKG   := goodkind.io/lm-review/internal/version

GO_MK_MODULES := go-build.mk go-release.mk

include bootstrap.mk

.DEFAULT_GOAL := check
