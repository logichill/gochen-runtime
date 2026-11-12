package quick

import (
	"reflect"

	"gochen/app/crud"
	"gochen/auth"
	"gochen/domain"
	"gochen/errors"
)

// App 创建 CRUD Application；资源名与授权器由调用方显式提供。
//
// 授权策略不在此内置：默认做法是在启动清单登记前选定显式授权器
// （auth.Compile / auth.New / auth.Allow）；声明式装配由 DeclaredApp 提供。
func App[T domain.IEntity[ID], ID comparable](
	repository crud.Repository[T, ID],
	options crud.Options[T, ID],
) (*crud.Application[T, ID], error) {
	if repository == nil || isNil(repository) {
		return nil, errors.NewCode(errors.InvalidInput, "repository cannot be nil")
	}
	return crud.NewApplication[T, ID](repository, options)
}

// DeclaredApp 按强类型规则编译授权并装配 CRUD；元数据来自仓储有效契约。
func DeclaredApp[T domain.IEntity[ID], ID comparable](repository crud.Repository[T, ID], options crud.Options[T, ID], rules []auth.Rule, dependencies auth.CompileOptions) (*crud.Application[T, ID], error) {
	if isNil(repository) {
		return nil, errors.NewCode(errors.InvalidInput, "repository cannot be nil")
	}
	if !isNil(options.Authorizer) {
		return nil, errors.NewCode(errors.InvalidInput, "declared application constructs its authorizer from rules")
	}
	descriptor, err := crud.Describe(options, true)
	if err != nil {
		return nil, err
	}
	dependencies.Metadata = repository.ExecutionDescriptor().Metadata()
	authorizer, err := auth.Compile(auth.Declaration{Descriptor: descriptor, ResourceAccess: auth.ResourceAccessData, Rules: rules}, dependencies)
	if err != nil {
		return nil, err
	}
	options.Authorizer = authorizer
	return crud.NewApplication(repository, options)
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
