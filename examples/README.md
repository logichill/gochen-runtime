# gochen-runtime/examples：Runtime 驱动与端到端示例

包含基于 `gochen-runtime` 驱动层与 Host 容器的端到端可运行演示：

- `domain/`：标准 CRUD、Audited CRUD、事件溯源与查询推导
- `auth/`、`security/`：动作授权、数据范围、分层安全与 SQL 存储
- `host/`、`di/`：Host 模块化容器、生命周期与依赖注入
- `infra/`：SQL Outbox、Projection Checkpoint、Snapshot 与 SQL Builder
- `process/`：Saga 编排、Workflow 流程引擎
- `quick/`：Quick 开箱即用门面
- `e2e/order_fulfillment`：综合端到端履约示例
