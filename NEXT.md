# next 核心候选

本分支用于审阅，不是发布版本。上游 Core 固定为：

- 仓库：https://github.com/logichill/gochen
- 提交：`28ac6f5505bef00b98b859183211469d85f7f52b`
- 迁移清单：https://github.com/logichill/gochen/blob/28ac6f5505bef00b98b859183211469d85f7f52b/docs/guides/core-next-migration.md

两个仓库平级检出，保留既有开发态 `replace gochen => ../gochen`；没有改成不存在的发布版本，也未修改 module path。单独从 Go proxy 安装尚不是本候选的交付方式。审阅锁定上面 commit，而不是假设任何时刻的 main 或 next 都兼容。

```sh
git clone --branch next https://github.com/logichill/gochen.git
git -C gochen checkout 28ac6f5505bef00b98b859183211469d85f7f52b
git clone --branch next https://github.com/logichill/gochen-runtime.git
cd gochen-runtime
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test -count=1 ./...
GOWORK=off go test -race -count=1 ./...
GOWORK=off make examples-smoke
GOWORK=off go run ./examples/typed-module
```

评审入口：`examples/business-actions/README.md`、`examples/typed-module/main.go`、`api/rest/command_binding.go`、`host/module/binding.go` 和 `host/README.md`。普通模块通过 `Binding(rest.Resource(application))` 同时贡献应用与路由；业务动作通过 `rest.Bind` 引用同一类型化 Command，命名用例保留普通 Go 主流程。生产认证应由可信组件提供；示例身份头仅用于本机演示。

已验证：独立 build/vet/full tests、全仓 race、全部既有 examples smoke。未执行 golangci-lint（云环境未安装该工具）。不会由这次候选改动自动迁移下游业务仓、数据库或 main。

本批新增只读能力与类型化业务绑定，原 `rest.Action` 已移除。审计、归属迁移与 ES 的执行语义保持；完整审计/删除能力拆分及 ES 显式状态模型尚未实施。具体迁移边界见 Core [业务能力说明](https://github.com/logichill/gochen/blob/28ac6f5505bef00b98b859183211469d85f7f52b/docs/guides/business-capabilities-next.md)。

联合验收同时发现并修复 net/http 的自动 OPTIONS 路由歧义：冲突模式会在 SealRoutes/Start 阶段返回配置错误，Handler 的无错误返回接口以 503 拒绝业务分发，不再等到首个请求时 panic。修改配置后必须重建 Server；没有通过移除 OPTIONS 或关闭入口核对掩盖冲突。
