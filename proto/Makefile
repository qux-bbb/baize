# Baize (白泽) — Protobuf 代码生成
# 用法: make gen (生成 Go + Rust 代码)

PROTO_DIR = .
GEN_DIR = gen

# Go output
GO_OUT = $(GEN_DIR)/go
GO_PKG = baize.io/proto/gen/go

# Rust output
RUST_OUT = $(GEN_DIR)/rust

# Proto files
PROTO_FILES = $(shell find $(PROTO_DIR) -name "*.proto")

.PHONY: all gen-go gen-rust clean

all: gen-go gen-rust

# ── Go ──────────────────────────────────────────────────
gen-go:
	@echo ">>> 生成 Go 代码..."
	mkdir -p $(GO_OUT)
	protoc \
		--proto_path=$(PROTO_DIR) \
		--go_out=$(GO_OUT) \
		--go_opt=paths=source_relative \
		--go-grpc_out=$(GO_OUT) \
		--go-grpc_opt=paths=source_relative \
		$(PROTO_FILES)
	@echo ">>> Go 代码已生成: $(GO_OUT)"
	@ls -la $(GO_OUT)/baize/v1/

# ── Rust ──────────────────────────────────────────────────
gen-rust:
	@echo ">>> 生成 Rust 代码..."
	mkdir -p $(RUST_OUT)
	protoc \
		--proto_path=$(PROTO_DIR) \
		--rust_out=$(RUST_OUT) \
		--grpc_out=$(RUST_OUT) \
		$(PROTO_FILES)
	@echo ">>> Rust 代码已生成: $(RUST_OUT)"
	@ls -la $(RUST_OUT)/

# ── Clean ──────────────────────────────────────────────────
clean:
	rm -rf $(GO_OUT) $(RUST_OUT)
	@echo ">>> 清理完成"
