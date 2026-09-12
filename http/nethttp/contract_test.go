package nethttp_test

import (
	"testing"

	runtimehttp "gochen-runtime/http"
	"gochen-runtime/http/contracttest"
	"gochen-runtime/http/nethttp"
)

func TestHTTPContract(t *testing.T) {
	contracttest.Run(t, func(cfg *runtimehttp.WebConfig) (contracttest.IServer, error) {
		return nethttp.NewServer(cfg), nil
	})
}
