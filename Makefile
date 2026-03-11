# ==============================================================================
# PairProxy Makefile
#
# 用法：
#   make              — 等同于 make build（构建当前平台）
#   make test         — 运行所有测试
#   make release      — 交叉编译全平台发布包
#   make help         — 显示帮助
#
# 依赖：
#   - Go 1.22+（使用完整路径或确保 go 在 PATH 中）
#   - zip / tar（发布打包用，release 目标需要）
# ==============================================================================

# --------------------------------------------------------------------------
# 变量
# --------------------------------------------------------------------------

MODULE  := github.com/l17728/pairproxy
BINDIR   := bin
DISTDIR  := dist
RELDIR   := release

# 二进制名称（Windows 下由交叉编译目标自动添加 .exe）
CPROXY  := cproxy
SPROXY  := sproxy

# 版本：优先从 git tag 读取，否则使用 "dev"
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILT   := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || echo "unknown")

# ldflags：注入版本信息到二进制
LDFLAGS := -s -w \
  -X $(MODULE)/internal/version.Version=$(VERSION) \
  -X $(MODULE)/internal/version.Commit=$(COMMIT) \
  -X $(MODULE)/internal/version.BuiltAt=$(BUILT)

# 交叉编译目标（OS/ARCH 对）
PLATFORMS := \
  linux/amd64 \
  linux/arm64 \
  darwin/amd64 \
  darwin/arm64 \
  windows/amd64

# Go 命令（支持环境变量覆盖，兼容 Windows 全路径）
GO ?= go

# --------------------------------------------------------------------------
# 默认目标
# --------------------------------------------------------------------------

.DEFAULT_GOAL := build
.PHONY: build test test-race test-cover lint vet fmt \
        release clean help tidy run-cproxy run-sproxy \
        bcrypt-hash

# --------------------------------------------------------------------------
# 构建（当前平台）
# --------------------------------------------------------------------------

## build: 构建 cproxy 和 sproxy（当前平台）
build: $(BINDIR)/$(CPROXY) $(BINDIR)/$(SPROXY)

$(BINDIR)/$(CPROXY): $(shell find cmd/cproxy internal -name '*.go' 2>/dev/null) | $(BINDIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/cproxy

$(BINDIR)/$(SPROXY): $(shell find cmd/sproxy internal -name '*.go' 2>/dev/null) | $(BINDIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $@ ./cmd/sproxy

$(BINDIR):
	mkdir -p $(BINDIR)

## build-dev: 构建全部四个二进制（含 mockllm/mockagent）到 release/，供本地测试使用
build-dev: $(RELDIR)
	$(GO) build -ldflags "$(LDFLAGS)" -o $(RELDIR)/$(SPROXY) ./cmd/sproxy
	$(GO) build -ldflags "$(LDFLAGS)" -o $(RELDIR)/$(CPROXY) ./cmd/cproxy
	$(GO) build -o $(RELDIR)/mockllm ./cmd/mockllm
	$(GO) build -o $(RELDIR)/mockagent ./cmd/mockagent
	@echo "Dev binaries in $(RELDIR)/"

$(RELDIR):
	mkdir -p $(RELDIR)

# --------------------------------------------------------------------------
# 测试
# --------------------------------------------------------------------------

## test: 运行所有测试（不含 race detector）
test:
	$(GO) test ./... -count=1

## test-race: 启用 race detector 运行测试（较慢）
test-race:
	$(GO) test ./... -count=1 -race

## test-cover: 生成 HTML 覆盖率报告（打开 coverage.html）
test-cover:
	$(GO) test ./... -count=1 -coverprofile=coverage.out -covermode=atomic
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## test-pkg: 测试单个包，用法：make test-pkg PKG=./internal/quota/...
test-pkg:
	$(GO) test -v -count=1 $(PKG)

# --------------------------------------------------------------------------
# 代码质量
# --------------------------------------------------------------------------

## vet: 运行 go vet
vet:
	$(GO) vet ./...

## fmt: 格式化所有 Go 代码
fmt:
	$(GO) fmt ./...

## lint: 运行 golangci-lint（需先安装：go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest）
lint:
	golangci-lint run ./...

## tidy: 整理 go.mod / go.sum
tidy:
	$(GO) mod tidy

# --------------------------------------------------------------------------
# 开发辅助
# --------------------------------------------------------------------------

## run-sproxy: 用示例配置启动 sproxy（开发用，需先复制 config/sproxy.yaml.example）
run-sproxy: $(BINDIR)/$(SPROXY)
	$(BINDIR)/$(SPROXY) start --config config/sproxy.yaml

## run-cproxy: 用示例配置启动 cproxy（开发用，需先复制 config/cproxy.yaml.example）
run-cproxy: $(BINDIR)/$(CPROXY)
	$(BINDIR)/$(CPROXY) start --config config/cproxy.yaml

## bcrypt-hash: 生成 admin 密码的 bcrypt hash（用于 sproxy.yaml admin.password_hash）
bcrypt-hash: $(BINDIR)/$(SPROXY)
	$(BINDIR)/$(SPROXY) hash-password

# --------------------------------------------------------------------------
# 交叉编译发布包
# --------------------------------------------------------------------------

## release: 交叉编译所有平台，打包到 dist/
release: clean-dist
	@mkdir -p $(DISTDIR)
	@$(foreach platform,$(PLATFORMS),\
		$(MAKE) _release-one GOOS=$(word 1,$(subst /, ,$(platform))) GOARCH=$(word 2,$(subst /, ,$(platform)));)
	@echo ""
	@echo "Release artifacts:"
	@ls -lh $(DISTDIR)/

# 内部目标：编译单个平台并打包
# 调用方式：make _release-one GOOS=linux GOARCH=amd64
_release-one:
	$(eval EXT     := $(if $(filter windows,$(GOOS)),.exe,))
	$(eval SUFFIX  := $(GOOS)-$(GOARCH))
	$(eval OUTDIR  := $(DISTDIR)/pairproxy-$(VERSION)-$(SUFFIX))
	@echo "Building $(SUFFIX)..."
	@mkdir -p $(OUTDIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
		$(GO) build -ldflags "$(LDFLAGS)" -o $(OUTDIR)/$(CPROXY)$(EXT) ./cmd/cproxy
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 \
		$(GO) build -ldflags "$(LDFLAGS)" -o $(OUTDIR)/$(SPROXY)$(EXT) ./cmd/sproxy
	@cp -r config $(OUTDIR)/
	@if [ "$(GOOS)" = "windows" ]; then \
		cd $(DISTDIR) && zip -qr pairproxy-$(VERSION)-$(SUFFIX).zip pairproxy-$(VERSION)-$(SUFFIX)/; \
	else \
		cd $(DISTDIR) && tar -czf pairproxy-$(VERSION)-$(SUFFIX).tar.gz pairproxy-$(VERSION)-$(SUFFIX)/; \
	fi
	@rm -rf $(OUTDIR)

# --------------------------------------------------------------------------
# 清理
# --------------------------------------------------------------------------

## clean: 删除构建产物（bin/）
clean:
	rm -rf $(BINDIR)

## clean-dist: 删除发布包（dist/）
clean-dist:
	rm -rf $(DISTDIR)

## clean-all: 删除所有构建产物和覆盖率文件
clean-all: clean clean-dist
	rm -f coverage.out coverage.html

# --------------------------------------------------------------------------
# 帮助
# --------------------------------------------------------------------------

## help: 显示所有可用目标
help:
	@echo "PairProxy Makefile — version: $(VERSION)"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
	@echo ""
	@echo "Variables:"
	@echo "  GO      Go binary path (default: go)"
	@echo "  PKG     Package for test-pkg (e.g. ./internal/quota/...)"
	@echo "  VERSION $(VERSION)"
	@echo "  COMMIT  $(COMMIT)"
