.PHONY: help build test vet fmt lint lint-full examples-smoke quality-local

help:
	@echo "gochen-runtime - 物理驱动与 Host 运行时"
	@echo ""
	@echo "可用命令:"
	@echo "  make build          - 编译全部包"
	@echo "  make test           - 运行全部测试"
	@echo "  make vet            - 静态检查"
	@echo "  make fmt            - 代码格式化"
	@echo "  make lint           - 静态代码审查"
	@echo "  make examples-smoke - 运行全部示例 smoke 测试"

build:
	GOWORK=off go build ./...

test:
	GOWORK=off go test -count=1 ./...

vet:
	GOWORK=off go vet ./...

fmt:
	GOWORK=off go fmt ./...

lint:
	GOWORK=off golangci-lint run --new ./...

lint-full:
	GOWORK=off golangci-lint run --new=false ./...

quality-local:
	GOWORK=off go build ./...
	GOWORK=off go vet ./...
	GOWORK=off golangci-lint run --new=false ./...
	GOWORK=off go test -count=1 ./...
	GOWORK=off go test -race -count=1 ./...

examples-smoke:
	@set -e; \
	for pkg in \
		./examples/auth/authorization \
		./examples/auth/http \
		./examples/auth/sqlstore \
		./examples/security/layering \
		./examples/di/basic \
		./examples/domain/audited \
		./examples/domain/audited_dimensions \
		./examples/domain/crud \
		./examples/domain/eventsourced \
		./examples/domain/transfer \
		./examples/domain/query/inferred \
		./examples/domain/query/filter \
		./examples/host/module_boot \
		./examples/quick \
		./examples/infra/outbox/sql \
		./examples/infra/projection/sql_checkpoint \
		./examples/infra/snapshot/basic \
		./examples/infra/sqlbuilder/basic \
		./examples/process/saga/basic \
		./examples/process/workflow/basic \
		./examples/e2e/order_fulfillment; do \
		echo "运行示例 $$pkg ..."; \
		MONITORING_ADDR= go run "$$pkg" >/dev/null || exit 1; \
	done; \
	echo "✅ 全部示例 smoke 测试通过！"
