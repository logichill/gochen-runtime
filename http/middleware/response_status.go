package middleware

import (
	"net/http"

	"gochen/errors"
	"gochen/httpx"
)

type statusCoder interface {
	StatusCode() int
}

func responseStatus(ctx httpx.IContext, err error) int {
	if err != nil {
		return errors.ToHTTPStatus(err)
	}
	if coder, ok := ctx.(statusCoder); ok {
		if status := coder.StatusCode(); status > 0 {
			return status
		}
	}
	return http.StatusOK
}
