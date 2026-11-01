package quick

import (
	"reflect"

	"gochen/app/crud"
	"gochen/domain"
	"gochen/errors"
)

// App 创建 CRUD Application；资源名与授权器由调用方显式提供。
//
// 授权策略不在此内置：默认做法是在启动清单登记前选定显式授权器
// （auth.Allow / auth/action / auth/scoped），避免隐式放行。
func App[T domain.IEntity[ID], ID comparable](
	repository crud.Repository[T, ID],
	options crud.Options[T, ID],
) (*crud.Application[T, ID], error) {
	if repository == nil || isNil(repository) {
		return nil, errors.NewCode(errors.InvalidInput, "repository cannot be nil")
	}
	return crud.NewApplication[T, ID](repository, options)
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return val.IsNil()
	default:
		return false
	}
}
