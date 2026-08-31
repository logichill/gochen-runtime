package rest

import (
	"context"
	"reflect"

	"gochen/domain/audited"
)

// IAuditedService 是 API 层用于"能力探测"的最小 audited 能力面。
//
// 说明：
//   - audited 扩展是一个聚合能力，实体为 audited 时必须整体满足；
//   - RouteBuilder 只依赖该最小方法集，不绑定具体 app/audited 实现。
type IAuditedService[T any, ID comparable] interface {
	Delete(ctx context.Context, id ID) error
	Purge(ctx context.Context, id ID) error
	Restore(ctx context.Context, id ID, by string) error

	ListDeleted(ctx context.Context, offset, limit int) ([]T, error)
	AuditTrail(ctx context.Context, id ID, offset, limit int) ([]audited.AuditRecord, error)

	AuditStore() audited.IAuditStore
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
