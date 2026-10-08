# Operation SSE 适配

`operationhttp.PrepareStreamHeaders`、`WriteSSEEvent` 和 `Stream` 将 Core 的 `app/operation.Result` 转为 HTTP SSE。轮询来源通过回调注入，通常调用同一个 `operation.Tracker`。

路由、鉴权、归属检查和持久化保留策略由接入方提供。Core 的 Runner 与 Tracker 不依赖 SSE。Stream 保留取消、心跳与终态结束行为，degraded 也属于终态。
