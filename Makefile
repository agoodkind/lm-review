BINARY := lm-review
CMD    := ./cmd/$(BINARY)
VPKG   := goodkind.io/lm-review/internal/version
LAUNCHD_LABEL := io.goodkind.lm-review-inference
SYSTEMD_UNIT  := lm-review-inference.service
LOG_PATH      := $(HOME)/Library/Logs/lm-review-inference.log

GO_MK_MODULES := go-build.mk go-release.mk go-service.mk

include bootstrap.mk

.DEFAULT_GOAL := check

.PHONY: proto deploy-inference inference-status
proto:
	protoc --proto_path=api --go_out=api/reviewpb --go_opt=paths=source_relative --go-grpc_out=api/reviewpb --go-grpc_opt=paths=source_relative review.proto
	protoc --proto_path=api/inferencepb --go_out=api/inferencepb --go_opt=paths=source_relative --go-grpc_out=api/inferencepb --go-grpc_opt=paths=source_relative inference.proto

deploy-inference:
	$(MAKE) BUILD_CHECKS=false install
	$(MAKE) service-install
	$(MAKE) service-restart

inference-status:
	$(MAKE) service-status
