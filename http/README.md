# http：标准库 net/http 服务器与中间件

`gochen-runtime/http` 是 Runtime 模块提供的物理 HTTP 运行实现，基于 Go 标准库 `net/http` 实现 `gochen/httpx` 契约，支持模块化路由分组、严格请求体校验与全链路可观测中间件。

`http.NewHTTPServer` 统一构建超时、请求头限制和 TLS 配置，`http.NewStaticHandler` 统一静态路径与符号链接校验；net/http 与 Contrib Gin 适配器共同复用这些实现。可信代理解析复用 `http/request.NewTrustedProxyChecker`。适配器行为测试由 `http/contracttest` 提供。

---

## 1. 核心特性

- **标准库高性能实现**：基于 Go 1.22+ `ServeMux` 实现 `httpx.IServer` 与 `httpx.IContext`，零第三方 Web 框架依赖；
- **丰富中间件库**（`http/middleware`）：
  - `Recovery`：Panic 捕获并规范化为 `errors.Internal`；
  - `AccessLog`：结构化访问日志记录；
  - `TraceID` / `RequestID`：全链路追踪 ID 注入与上下文绑定；
  - `CORS`：安全的跨域资源共享；
  - `RateLimit` / `CircuitBreaker`：限流与熔断防护；
  - `Timeout`：请求超时控制，要求适配器支持异步响应隔离；Runtime nethttp 支持，Contrib Gin 返回 `Unsupported`；
  - `SecurityHeaders`：安全响应头注入；
- **严格 JSON 绑定**：`BindJSON` 默认拒绝未知字段并禁止尾随数据，避免潜在安全与拼写隐患；
- **请求体大小防护**：内置最大请求体限制（默认 10MB，支持按端点或全局配置）。

`Timeout` 的并发名额保持到 handler 真正退出。超时后的 `Write` / `ReadFrom` 不再写入真实响应；缓冲提交、流式切换、Flush 和超时响应的写入错误会向外返回，原生 `Flush()` 的错误在中间件收尾时返回。已经开始的网络写入由服务器 `WriteTimeout` 约束。Gin 使用池化上下文，不能在中间件返回后继续异步执行其处理链，因此当前不支持与该中间件组合。

静态目录、单文件及挂载点跳转均执行全局中间件，只接受 GET/HEAD；目录跳转保留查询参数。

---

## 2. 快速使用

```go
package main

import (
    "gochen/httpx"
    "gochen-runtime/http"
    "gochen-runtime/http/middleware"
    "gochen-runtime/http/nethttp"
)

func main() {
    // 1. 初始化 HTTP 服务器
    server := nethttp.NewServer(&http.WebConfig{
        Host: "0.0.0.0",
        Port: 8080,
    })

    // 2. 装配全局中间件
    server.Use(
        middleware.Recovery(),
        middleware.RequestID(),
        middleware.AccessLog(),
    )

    // 3. 注册路由
    api := server.Group("/api/v1")
    api.GET("/ping", func(c httpx.IContext) error {
        return httpx.WriteSuccess(c, map[string]string{"message": "pong"})
    })

    // 4. 启动服务
    _ = server.Start(":8080")
}
```

---

## 3. 相关文档

- Core HTTP 抽象与统一响应助手：`gochen` 仓库 `httpx/README.md`
- 自动化 REST CRUD 路由生成：[`gochen-runtime/api/rest/README.md`](../api/rest/README.md)
