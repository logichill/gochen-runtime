# next 核心候选

本分支用于审阅，不是发布版本。上游 Core 固定为：

- 仓库：https://github.com/logichill/gochen
- 提交：`c17d2d2df1ac18e328e4060b18529328c058ff62`
- 迁移清单：https://github.com/logichill/gochen/blob/c17d2d2df1ac18e328e4060b18529328c058ff62/docs/guides/core-next-migration.md

两个仓库平级检出，保留既有开发态 `replace gochen => ../gochen`；没有改成不存在的发布版本，也未修改 module path。单独从 Go proxy 安装尚不是本候选的交付方式。审阅锁定上面 commit，而不是假设任何时刻的 main 或 next 都兼容。

```sh
git clone --branch next https://github.com/logichill/gochen.git
git -C gochen checkout c17d2d2df1ac18e328e4060b18529328c058ff62
git clone --branch next https://github.com/logichill/gochen-runtime.git
cd gochen-runtime
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test -count=1 ./...
GOWORK=off go test -race -count=1 ./...
GOWORK=off make examples-smoke
GOWORK=off go run ./examples/typed-module
```

评审入口：`examples/typed-module/main.go`、`host/module_builder.go`、`host/module/application.go` 和 `host/README.md`。普通模块只需显式构造 Repository/Application/Routes，再通过 Module 登记同一实例。生产认证应由可信组件提供；示例身份头仅用于本机演示。

已验证：独立 build/vet/full tests、全仓 race、全部既有 examples smoke。未执行 golangci-lint（云环境未安装该工具）。不会由这次候选改动自动迁移下游业务仓、数据库或 main。
