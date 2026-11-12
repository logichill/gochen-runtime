package rest

import (
	"net/http"

	"gochen/contextx"
	"gochen/httpx"
)

// wrapResponse 统一标准资源和命令入口的错误、提交后降级及请求体上限。
func wrapResponse(executor httpx.Handler, response ResponseOptions, maxBodySize int64) httpx.Handler {
	errorHandler := response.ErrorHandler
	if errorHandler == nil {
		errorHandler = DefaultErrorHandler
	}
	wrapper := response.ResponseWrapper
	if wrapper == nil {
		wrapper = DefaultResponseWrapper
	}

	return func(c httpx.IContext) error {
		if maxBodySize > 0 {
			c.Set(httpx.MaxBodySizeKey, httpx.ValueOf(maxBodySize))
		}
		if err := executor(c); err != nil {
			if contextx.IsAfterCommitError(err) {
				c.SetHeader("X-Execution-Status", "degraded")
				return c.JSON(http.StatusOK, httpx.JSONValue(wrapper(nil)))
			}
			response := errorHandler(c, err)
			if response == nil {
				response = DefaultErrorHandler(c, err)
			}
			return response.Send(c)
		}
		return nil
	}
}
