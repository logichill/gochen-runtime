package rest

import (
	"reflect"
)

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
