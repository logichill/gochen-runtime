package middleware

import (
	"net/http"
	"testing"

	"gochen/errors"
)

type statusContext struct {
	*stubContext
	statusCode int
}

func (c *statusContext) StatusCode() int { return c.statusCode }

func TestResponseStatus(t *testing.T) {
	tests := []struct {
		name string
		ctx  *statusContext
		err  error
		want int
	}{
		{
			name: "defaults to ok",
			ctx:  &statusContext{stubContext: &stubContext{}},
			want: http.StatusOK,
		},
		{
			name: "uses context status",
			ctx:  &statusContext{stubContext: &stubContext{}, statusCode: http.StatusCreated},
			want: http.StatusCreated,
		},
		{
			name: "ignores invalid context status",
			ctx:  &statusContext{stubContext: &stubContext{}, statusCode: -1},
			want: http.StatusOK,
		},
		{
			name: "error status takes precedence",
			ctx:  &statusContext{stubContext: &stubContext{}, statusCode: http.StatusCreated},
			err:  errors.NewCode(errors.NotFound, "missing"),
			want: http.StatusNotFound,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := responseStatus(test.ctx, test.err); got != test.want {
				t.Fatalf("responseStatus() = %d, want %d", got, test.want)
			}
		})
	}
}
