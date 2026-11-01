# api：HTTP 协议适配层

`gochen-runtime/api` 把 Core 的应用契约映射为对外 HTTP 协议。当前包含一个子包：

- `api/rest`：按 `Application.Descriptor()` 声明的动作生成标准 REST 路由；详细规则见 [`rest/README.md`](rest/README.md)。

## 在启动链路中的位置

```text
Host.StartBackground
  └─ Host.registerModuleRoutes
       └─ module.RegisterRoutes
            └─ rest.Mount / RouteBuilder.Register
                 └─ RouteBuilder.prepare
                      └─ Specs → mount → http.RegisterRoute
```

`rest` 不参与 Host 生命周期，也不持有容器。模块通过 `RouteRegistrar` 把应用挂载到 Host 注入的 `httpx.IRouteGroup`；`quick.RESTRegistrar` 是同一入口的便捷封装。

## 依赖关系

- 上游：`host.Module(...).RouteRegistrar(...)`、`quick.RESTRegistrar(...)`。
- Core 契约：`app/crud`、`app/audited`、`auth`、`app/query`、`codec`、`contextx`、`httpx`。
- Runtime 物理层：`gochen-runtime/http`、`http/middleware`、`http/request`。
- 持久化：由 Application 持有的 Repository 完成；`api/rest` 只处理协议绑定、路由、授权入口与响应映射。
