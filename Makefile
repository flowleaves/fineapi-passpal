# PassPal —— 个人凭据管理系统
#
# 常用目标：
#   make web      构建前端（产出 web/dist）
#   make build    构建二进制（含内嵌前端）
#   make run      本地开发运行
#   make test     运行 Go 单元测试
#   make check    编译 + vet + 单元测试
#   make docker   构建镜像
#   make hash     生成管理员密码 hash
#   make clean    清理构建产物

BINARY      := passpal
GO          ?= go
NPM         ?= npm
WEB_DIR     := web
DIST_DIR    := $(WEB_DIR)/dist

# 生产构建参数：无 cgo、去符号表
LDFLAGS := -s -w
BUILDFLAGS := -trimpath

.PHONY: help web build run test check vet fmt tidy docker up down logs hash clean

help:
	@grep -E '^#   ' $(MAKEFILE_LIST) | sed 's/^#   //'

# 构建前端静态资源
web:
	cd $(WEB_DIR) && $(NPM) ci --include=optional --no-audit --no-fund || $(NPM) install --include=optional --no-audit --no-fund
	cd $(WEB_DIR) && $(NPM) run build

# 构建二进制（要求 web/dist 已存在）
build:
	CGO_ENABLED=0 $(GO) build $(BUILDFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/passpal

# 本地运行（开发模式，需先 export 环境变量）
run:
	$(GO) run ./cmd/passpal serve

test:
	$(GO) test ./... -count=1

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

tidy:
	$(GO) mod tidy

check: vet test build

docker:
	docker build -t passpal:latest .

up:
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f --tail=100

# 生成管理员密码 hash（写入 .env 的 ADMIN_PASSWORD_HASH）
hash:
	@$(GO) run ./cmd/passpal hash-password

clean:
	rm -rf $(BINARY) $(BINARY).exe $(DIST_DIR)
