package module

import (
	"gochen-runtime/host/module/initcap"
	"gochen/httpx"
)

// IHTTPOptions 暴露模块 HTTP 挂载 group 的能力。
type IHTTPOptions interface {
	MountGroup() httpx.IRouteGroup
}

var (
	// HTTPKey 是 HTTP 挂载能力的类型化键。
	HTTPKey = initcap.NewKey[IHTTPOptions]("host.module.http")
)
