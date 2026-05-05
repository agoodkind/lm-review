# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# lm-review Makefile.
# Build/lint/release pipeline lives in go-makefile and is fetched at runtime.
# Edit go-makefile to change pipeline behavior; consumers pick up on next build.

# Identity
BINARY := lm-review
CMD    := ./cmd/$(BINARY)
VPKG   := goodkind.io/lm-review/internal/version

# Pipeline modules (fetched + included by go.mk)
GO_MK_MODULES := go-build.mk go-release.mk

include bootstrap.mk

.DEFAULT_GOAL := check

# Project-local
.PHONY: setup-hooks review-diff review-quick review-pr review-deep review-ultra review-repo

setup-hooks:
	git config core.hooksPath .githooks
	@echo "git hooks configured"

review-diff:
	lm-review diff

review-quick:
	lm-review diff --depth quick

review-pr:
	lm-review pr

review-deep:
	lm-review diff --depth deep &

review-ultra:
	lm-review diff --depth ultra &

review-repo:
	lm-review repo --async
