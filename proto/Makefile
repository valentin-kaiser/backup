# -------- Settings --------
PROTOC ?= protoc

# Paths
GO_GEN_DIR    = ./gen/go
GO_OUT_DIR    = ../pkg/proto
GO_MODULE     = github.com/Valentin-Kaiser/backup/pkg/proto
TS_GEN_DIR    = ./gen/ts
TS_OUT_DIR    = ../worker/portal/ui/src/app/global/model
SPECS_GEN_DIR = ./gen/specs
SPECS_OUT_DIR = ../docs/openapi

# Protos. Keep paths relative to PROTO_PATH so nested, versioned packages retain
# their import and generated-output layout.
PROTO_FILES := $(shell find . -type f -name '*.proto' -print | sort | sed 's|^\./||')
PROTO_PATH  := $(shell pwd)
PROTOC_GEN_TS_PROTO ?= /usr/lib/node_modules/ts-proto/protoc-gen-ts_proto

# -------- Targets --------
.PHONY: generate proto proto-go proto-ts clean restore

proto: pre-gen proto-go proto-ts post-gen clean

# Run the generation in a Docker container to ensure consistent tooling across environments.
generate:
	docker run --rm \
	  -v $(shell git rev-parse --show-toplevel):/app/ \
	  -w /app/proto \
	  ghcr.io/valentin-kaiser/protocdock:latest \
	  make proto

proto-go:
	@mkdir -p $(GO_GEN_DIR)
	$(PROTOC) -I $(PROTO_PATH) \
	  --go_out=$(GO_GEN_DIR) \
	  --go_opt=module=$(GO_MODULE) \
	  --go-xrpc_out=$(GO_GEN_DIR) \
	  --go-xrpc_opt=module=$(GO_MODULE),openapi_dir=../specs,schema_ts=../ts/xrpc_schema.ts \
	  --unsafe_allow_out_dir_escape \
	  $(PROTO_FILES)

proto-ts:
	@mkdir -p $(TS_GEN_DIR)
	$(PROTOC) -I $(PROTO_PATH) \
	  --plugin=$(PROTOC_GEN_TS_PROTO) \
	  --ts_proto_out=$(TS_GEN_DIR) \
	  --ts_proto_opt=esModuleInterop=true,useJsonName=true,outputServices=generic-definitions,exportCommonSymbols=false \
	  $(PROTO_FILES)

pre-gen:
	@rm -rf $(GO_GEN_DIR)
	@rm -rf $(TS_GEN_DIR)
	@rm -rf $(SPECS_GEN_DIR)
	@rm -rf $(GO_OUT_DIR)
	@rm -rf $(TS_OUT_DIR)
	@rm -rf $(SPECS_OUT_DIR)

post-gen:
	@mkdir -p $(GO_OUT_DIR)
	@mkdir -p $(TS_OUT_DIR)
	@mkdir -p $(SPECS_OUT_DIR)
	cp -r $(GO_GEN_DIR)/* $(GO_OUT_DIR)/
	cp -r $(TS_GEN_DIR)/* $(TS_OUT_DIR)/
	cp -r $(SPECS_GEN_DIR)/* $(SPECS_OUT_DIR)/

clean:
	rm -rf $(GO_GEN_DIR)
	rm -rf $(TS_GEN_DIR)
	rm -rf $(SPECS_GEN_DIR)

restore:
	git restore $(GO_OUT_DIR)
	git restore $(TS_OUT_DIR)
	git restore $(SPECS_OUT_DIR)
