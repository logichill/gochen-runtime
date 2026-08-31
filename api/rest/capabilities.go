package rest

import (
	"gochen/app/crud"
)

// batchWriter 为批量写路由选择 batch writer。
//
// 说明：批量写是 Application 的**可选能力**，通过类型断言探测，
// 未实现时在路由注册期 fail-fast（见 router.go）。
func (rb *RouteBuilder[T, ID]) batchWriter() (crud.IBatchWriter[T, ID], bool) {
	writer, ok := any(rb.service).(crud.IBatchWriter[T, ID])
	return writer, ok
}
