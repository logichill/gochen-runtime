package rest

import (
	"gochen/httpx"
)

type jsonResponse struct {
	status int
	body   any
}

func (r *jsonResponse) Send(ctx httpx.IContext) error {
	return ctx.JSON(r.status, httpx.JSONValue(r.body))
}

func successResponse(data any) httpx.IResponse {
	return &jsonResponse{
		status: 200,
		body:   httpx.NewSuccessMessage(data),
	}
}
