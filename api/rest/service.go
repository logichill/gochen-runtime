package rest

import (
	"context"
	"reflect"

	appaudited "gochen/app/audited"
)

// IAuditedService 是 API 层用于"能力探测"的最小 audited 能力面。
//
// 说明：
//   - audited 扩展是一个聚合能力，实体为 audited 时必须整体满足；
//   - RouteBuilder 只依赖该最小方法集，不绑定具体 app/audited 实现。
type IAuditedService[T any, ID comparable] interface {
	Delete(ctx context.Context, id ID) error
	appaudited.IAuditOperations[T, ID]
}

func isNilService(svc any) bool {
	if svc == nil {
		return true
	}
	rv := reflect.ValueOf(svc)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}
