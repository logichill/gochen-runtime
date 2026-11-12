# 订单审批：一个用例，两个入口

本例把订单审批写成普通业务调用：

```go
approval, err := approver.Approve(ctx, ApproveOrder{
    ID: 101, ExpectedVersion: 7, Reason: "Budget confirmed",
})
```

调用方只看到审批意图和 `Approval` DTO，不必选择框架动作、传授权规约或编写 Hook。

## 代码顺序

- `order.go`：`Order.Approve` 管理业务规则，只允许 `pending → approved`，理由不能为空。失败不修改对象。
- `approval.go`：`OrderApprover.Approve` 是业务入口。用例显式执行事务、加载、检查调用方版本、领域转换、保存和 DTO 映射。
- `assembly.go`：仅在组合根使用 `auth.Compile`、一个 `app.Command` 和 `CommandRepository`。业务只拿到窄的订单访问接口及 `repo.WithinTx`，无法漏传授权条件。
- `main.go`：创建独立内存 SQLite 和 `repo/lite` 仓储，装配后先直接审批订单 102，再启动回环 HTTP 服务。

HTTP 绑定直接引用这一个 Command：

```go
approver := &OrderApprover{execute: command.Execute}
orders, err := host.Module("orders").
    Binding(rest.Bind(http.MethodPost, "/:id/approve", command, decodeApprove)).
    Build()
```

模块默认挂载在 `/api/v1/orders`，所以绑定路径只写 `/:id/approve`。

`rest.Bind` 从 Command 取得冻结的授权元数据，模块同时登记应用和路由。HTTP 只解码传输输入，没有第二个动作字符串，也没有获得大 Application 后自行选择执行路径的 handler。直接调用仍执行同一个 Command，并使用调用时的可信身份重新授权。

## 运行

在 Runtime 仓库目录执行，需要相邻的 `../gochen` Core：

```sh
GOWORK=off go run ./examples/business-actions -port 8080
```

启动时会输出订单 102 的直接审批结果。HTTP 审批订单 101：

```sh
curl -i http://127.0.0.1:8080/api/v1/orders/101/approve \
  -H 'Content-Type: application/json' \
  -H 'X-Demo-Role: approver' \
  -d '{"expected_version":7,"reason":"Budget confirmed"}'
```

成功响应的 `data` 是 `{"order_id":101,"version":8,"status":"approved"}`。再次提交旧版本得到 409。按 Ctrl-C 退出后，Host 停止，组合根关闭数据库，数据全部消失。

演示身份 `approver` 有审批权限且属于部门 10；`reader` 只有读权限；缺少已知身份时拒绝访问。`X-Demo-Role` **不是生产认证方案**，此服务只监听 `127.0.0.1`。生产环境必须改用可信认证结果，不能相信客户端提交的身份、部门或权限。

初始订单：101/102/105 属于部门 10，版本 7、待审批；103 属于部门 20；104 已审批。部门来自认证上下文，授权规约下推到 SQL，部门维度受普通更新保护。范围外订单统一返回 404，避免泄露其存在性；无身份返回 401，有身份但缺动作权限返回 403。

## 验证

```sh
GOWORK=off go test -count=1 ./examples/business-actions
GOWORK=off go test -race -count=1 ./examples/business-actions
```

测试通过真实 Host、HTTP 适配器、SQLite 和 SQL 仓储验证：

- HTTP 与直接调用都拒绝无身份、无审批权限、跨部门、过期版本、非法状态和空理由。
- 每次拒绝后逐行核对数据库不变。
- HTTP 与直接调用分别审批成功，DTO、保存状态及版本推进一致，HTTP 的提交对后续直接调用可见。
- SQLite `AFTER UPDATE` 触发器用 `RAISE(FAIL)` 在写入已发生后失败，验证显式事务回滚，并能用原版本重试。
- Host 停止不关闭外部传入的数据库连接，连接由组合根释放。

事务回调失败会触发回滚；提交或底层错误仍向上传递。若提交结果不明，应先核对状态，不能把所有错误都视为安全重试信号。上述触发器测试只证明可确认的语句失败及其回滚路径。

本例只演示单资源、当前状态存储和直接进程内调用。它没有注册定时 Job，也不承诺持久回执、幂等重放或 Outbox；没有为审批引入删除生命周期、事件溯源或查询框架。
