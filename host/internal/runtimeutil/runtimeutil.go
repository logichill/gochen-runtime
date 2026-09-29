package runtimeutil

import (
	"fmt"
	"reflect"

	"gochen/errors"
)

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

// WrapModuleErr 为 err 附加模块上下文。err 已是 AppError 时沿用其错误码与细节，
// 否则使用 fallback 错误码。
func WrapModuleErr(moduleID string, err error, message string, fallback errors.ErrorCode) errors.IAppError {
	if err == nil {
		return nil
	}

	var appErr *errors.AppError
	if errors.As(err, &appErr) && appErr != nil {
		// 保留完整原始链作为 cause：直接包装 appErr 会丢掉 errors.Join 的兄弟错误，
		// 使上层无法再用 errors.Is/As 检出它们。
		return errors.NewCodeWithCause(appErr.Code(), fmt.Sprintf("%s: %s", message, appErr.Message()), err).
			WithDetails(appErr.Details()).
			WithContext("module", moduleID)
	}
	return errors.Wrap(err, fallback, message).WithContext("module", moduleID)
}
