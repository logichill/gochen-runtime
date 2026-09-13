package runtimeutil

import "reflect"

// TypeString 返回 v 的动态类型字符串（nil 返回 "<nil>"）。
func TypeString(v any) string {
	if v == nil {
		return "<nil>"
	}
	return reflect.TypeOf(v).String()
}

// IsTypedNil 检查接口值是否为 typed nil（接口非空但底层值为 nil）。
func IsTypedNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Interface, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// NormalizeError 将 typed nil error 归一化为真正的 nil。
func NormalizeError(err error) error {
	if IsTypedNil(err) {
		return nil
	}
	return err
}
