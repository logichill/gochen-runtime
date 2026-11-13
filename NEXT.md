# next 核心候选

本分支用于审阅，不是发布版本。上游 Core 固定为：

- 仓库：https://github.com/logichill/gochen
- 提交：`10409ae4765e75ac0cb1267a2e6dd0cc09300a58`
- 迁移清单：https://github.com/logichill/gochen/blob/10409ae4765e75ac0cb1267a2e6dd0cc09300a58/docs/guides/core-next-migration.md

两个仓库平级检出，保留既有开发态 `replace gochen => ../gochen`；没有改成不存在的发布版本，也未修改 module path。单独从 Go proxy 安装尚不是本候选的交付方式。审阅锁定上面 commit，而不是假设任何时刻的 main 或 next 都兼容。

```sh
git clone --branch next https://github.com/logichill/gochen.git
git -C gochen checkout 10409ae4765e75ac0cb1267a2e6dd0cc09300a58
git clone --branch next https://github.com/logichill/gochen-runtime.git
cd gochen-runtime
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test -count=1 ./...
GOWORK=off go test -race -count=1 ./...
GOWORK=off make examples-smoke
GOWORK=off go run ./examples/typed-module
```

评审入口：`examples/business-actions/README.md`、`examples/typed-module/main.go`、`api/rest/endpoint_binding.go`、`host/module/binding.go` 和 `host/README.md`。普通模块通过 `Binding(rest.Resource(application))` 同时贡献应用与路由；业务动作通过 `rest.Bind` 消费同一具名业务对象的类型化执行与授权绑定，内部复用 CRUD/ES 应用。生产认证应由可信组件提供；示例身份头仅用于本机演示。

已验证：独立 build/vet/full tests、全仓 race、全部既有 examples smoke。未执行 golangci-lint（云环境未安装该工具）。不会由这次候选改动自动迁移下游业务仓、数据库或 main。

当前候选保留独立审计、显式 ES 状态模型和 EventHistory 命名；删除生命周期已统一回 CRUD Application，不再提供独立 deletion.Application 或 WithDeletion。REST 通过 WithAudit 独立组合历史读取，通过 WithRoutes 精确选择 HTTP 操作；原 rest.Action、app/audited 和 domain/audited 不再提供。持久字段、事务审计、Transfer 与旧事件格式保持。具体迁移边界见 Core [业务能力说明](https://github.com/logichill/gochen/blob/10409ae4765e75ac0cb1267a2e6dd0cc09300a58/docs/guides/business-capabilities-next.md)。

联合验收同时发现并修复 net/http 的自动 OPTIONS 路由歧义：冲突模式会在 SealRoutes/Start 阶段返回配置错误，Handler 的无错误返回接口以 503 拒绝业务分发，不再等到首个请求时 panic。修改配置后必须重建 Server；没有通过移除 OPTIONS 或关闭入口核对掩盖冲突。

新增审阅入口：`examples/domain/explicit-eventsourced`、`api/rest/capabilities.go`、`db/orm/repo/capability_composition_test.go`、`eventing/store/sqlstore/state_repository_test.go`。显式 ES 复用原 EventStore，值/指针事件在真实 SQLite 和 MemoryStore 中 roundtrip 一致；不同 Go 事件类型即使同名也拒绝。ES 历史新动作是 event_history_read，部署端需显式迁移历史权限及自定义路由，CRUD audit_read 保持。

授权收敛批次：移除 Core 公共 Command/Grant 特殊执行路径，普通业务保留具名方法，并复用 CRUD/ES 固定授权生命周期。`rest.Bind` 接收同一业务对象的执行与授权声明，不再依赖具体 Command 类型；这是可信装配合同，不是任意实现的安全沙箱。多资源服务显式预检后各应用仍正式授权，只有已证明的同源同一事务才保证后续拒绝时整体回滚。

新增回归覆盖显式 StateApplication 的真实 SQLite 授权、最终回放状态与隔离/范围/赋值检查、同名异型事件追加前拒绝，以及 net/http 和 Gin 独立审计与回收站查询的分页连续性。最终三仓独立 build/vet/test/race 与示例检查均通过；全量测试使用 `GOMAXPROCS=2 GOFLAGS=-p=1` 控制并发，未改变计时测试或断言。迁移细节见 [next 授权迁移](https://github.com/logichill/gochen/blob/10409ae4765e75ac0cb1267a2e6dd0cc09300a58/docs/guides/next-authorization-migration.md)。

删除模式是写应用的必填构造配置：`crud.DeleteSoft` 只软删，重复调用不升级；`crud.DeletePurge` 令业务 Delete/批量 Delete 使用完整 purge 权限、钩子、事务和审计。零值、未知值或缺能力均构造失败，不为旧调用补硬删除默认值。Purge 始终物理清除，包括旧软删记录；恢复和回收站能力独立选配，审计历史仍独立。默认业务 HTTP DELETE 遵循这个明确模式：未公开 `/:id/purge` 不表示硬模式不会永久删除。普通创建/更新及自定义执行不能改变删除状态，生命周期保留动作只经专用入口执行；SQL/GORM 回归核对批量失败时实体、版本与审计一起回滚。业务下游升级前须逐一迁移模式、权限/钩子和路由选择，本候选不改动现有生产数据。
