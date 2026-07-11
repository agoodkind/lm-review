BINARY := lm-review
CMD    := ./cmd/$(BINARY)
VPKG   := goodkind.io/lm-review/internal/version
LAUNCHD_LABEL := io.goodkind.lm-review-inference
SYSTEMD_UNIT  := lm-review-inference.service
LOG_PATH      := $(HOME)/Library/Logs/lm-review-inference.log

GO_MK_MODULES := go-build.mk go-release.mk go-service.mk

include bootstrap.mk

.DEFAULT_GOAL := check

.PHONY: proto deploy-inference inference-helper-check inference-preflight inference-post-restart inference-status
proto:
	protoc --proto_path=api --go_out=api/reviewpb --go_opt=paths=source_relative --go-grpc_out=api/reviewpb --go-grpc_opt=paths=source_relative review.proto
	protoc --proto_path=api/inferencepb --go_out=api/inferencepb --go_opt=paths=source_relative --go-grpc_out=api/inferencepb --go-grpc_opt=paths=source_relative inference.proto

deploy-inference:
	$(MAKE) BUILD_CHECKS=false install
	$(MAKE) inference-preflight
	$(MAKE) service-install
	$(MAKE) service-restart
	$(MAKE) inference-post-restart

service-check: inference-helper-check

inference-helper-check:
	@test -x ./scripts/inference-service-check.sh || { echo "service-install: scripts/inference-service-check.sh is missing or not executable" >&2; exit 1; }

inference-preflight:
	@./scripts/inference-service-check.sh preflight "$(INSTALL_BIN)" "$(LAUNCHD_LABEL)" "$(SYSTEMD_UNIT)"

inference-post-restart:
	@./scripts/inference-service-check.sh post-restart "$(INSTALL_BIN)" "$(LAUNCHD_LABEL)" "$(SYSTEMD_UNIT)"

inference-status:
	$(MAKE) service-status
