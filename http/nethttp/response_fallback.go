package nethttp

import (
	"fmt"

	"gochen/httpx"
)

// WriteErrorResponse 写入标准错误响应；若响应已写出则跳过。
func WriteErrorResponse(ctx httpx.IContext, err error) error {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Get(httpContextKeyResponseWritten); ok {
		if written, _ := httpx.ValueAs[bool](v); written {
			return nil
		}
	}

	if err == nil {
		return nil
	}

	if jerr := httpx.WriteError(ctx, err); jerr != nil {
		if v, ok := ctx.Get(httpContextKeyResponseWritten); ok {
			if written, _ := httpx.ValueAs[bool](v); written {
				return nil
			}
		}
		status, payload := httpx.EncodeErrorResponse(ctx, err)
		if payload == nil {
			return nil
		}
		_ = ctx.String(status, fmt.Sprintf("%s: %s", payload.Code, payload.Message))
	}
	ctx.Set(httpContextKeyResponseWritten, httpx.ValueOf(true))
	return nil
}
