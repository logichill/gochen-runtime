package nethttp

import (
	"net/http"

	"gochen/httpx"
)

// JSONResponse 表示 JSON 响应。
type JSONResponse struct {
	status int
	body   any
}

func (r *JSONResponse) Send(ctx httpx.IContext) error {
	return ctx.JSON(r.status, httpx.JSONValue(r.body))
}

// SuccessResponse 构造标准成功响应（code=ok）。
func SuccessResponse(data any) httpx.IResponse {
	return &JSONResponse{status: http.StatusOK, body: httpx.NewSuccessMessage(data)}
}

type errorResponse struct {
	err error
}

// Send 按统一错误编码写出响应。
func (r *errorResponse) Send(ctx httpx.IContext) error {
	status, payload := httpx.EncodeErrorResponse(ctx, r.err)
	if payload == nil {
		return nil
	}
	return ctx.JSON(status, httpx.JSONValue(payload))
}

func ErrorResponse(err error) httpx.IResponse {
	return &errorResponse{err: err}
}
