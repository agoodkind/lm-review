BINARY := lm-review
CMD    := ./cmd/$(BINARY)
VPKG   := goodkind.io/lm-review/internal/version

GO_MK_MODULES := go-build.mk go-release.mk

include bootstrap.mk

.DEFAULT_GOAL := check

.PHONY: proto
proto:
	protoc --proto_path=api --go_out=api/reviewpb --go_opt=paths=source_relative --go-grpc_out=api/reviewpb --go-grpc_opt=paths=source_relative review.proto
	protoc --proto_path=api/judgepb --go_out=api/judgepb --go_opt=paths=source_relative --go-grpc_out=api/judgepb --go-grpc_opt=paths=source_relative judge.proto
