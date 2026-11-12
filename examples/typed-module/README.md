# typed Module：一条可以追踪的普通 CRUD 主线

运行（需要同级 `gochen` 候选分支；本例不会访问现有数据库）：

```sh
GOWORK=off go run ./examples/typed-module -port 8080
```

组合根创建独立 SQLite 内存表及 ORM，再将 ORM 交给模块 ctor。ctor 只创建 Repository 和 Application 一次，通过 `host.Module("notes").Binding(rest.Resource(application)).Build()` 同时贡献应用登记与标准路由，不再重复登记同一应用。Host 核对真实入口后启动。Ctrl-C 先停止 Host，组合根随后关闭连接。

模块 ID `notes`、用例资源 `note` 和表名 `notes` 是不同事实，代码显式声明，不从路径猜权限。表、ID 字符串、版本与响应格式继续使用现有协议。本例不需要软删除、审计或 ES，也不增加这些能力。

仅监听回环地址。本例的 `X-Demo-Role` 是为了演示应用授权而提供的**不安全示例身份入口**，不是真实认证，绝不能用于生产或暴露到网络。正式应用应替换为可信认证组件，不能信任客户端自报角色。

```sh
# 无身份：401；reader 只可 read/list，editor 可操作 note。
curl -i http://127.0.0.1:8080/api/v1/notes
curl -H 'Content-Type: application/json' -H 'X-Demo-Role: editor' \
  -d '{"id":"note-1","title":"first"}' http://127.0.0.1:8080/api/v1/notes
curl -H 'X-Demo-Role: reader' http://127.0.0.1:8080/api/v1/notes/note-1
curl -X PUT -H 'Content-Type: application/json' -H 'X-Demo-Role: editor' \
  -d '{"title":"second","version":0}' http://127.0.0.1:8080/api/v1/notes/note-1
curl -X DELETE -H 'X-Demo-Role: editor' http://127.0.0.1:8080/api/v1/notes/note-1
```

```sh
GOWORK=off go test -count=1 ./examples/typed-module
```

测试通过真实 TCP HTTP 请求验证 CRUD、无身份/无权限拒绝、乐观版本冲突、真实路由清单、取消停止以及注入数据库不被 Host 关闭。连接只由组合根拥有。业务审计和 ES 的独立构造与事务回归见 Core 的 `app/audited` 与 `app/eventsourced`，不要把示例身份入口或建表代码复制到生产。
