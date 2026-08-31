package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gochen-runtime/http/nethttp"
	"gochen/errors"
)

func TestParseQuery(t *testing.T) {
	type query struct {
		Limit int    `query:"limit"`
		Scope string `query:"scope"`
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/items?limit=25&scope=active", nil)
	rec := httptest.NewRecorder()
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	bound, err := ParseQuery[query](ctx)
	if err != nil {
		t.Fatalf("ParseQuery returned error: %v", err)
	}
	if bound.Limit != 25 || bound.Scope != "active" {
		t.Fatalf("unexpected bound query: %+v", bound)
	}
}

func TestParseQueryWithDefaults(t *testing.T) {
	type query struct {
		Format string `query:"format"`
		Limit  int    `query:"limit"`
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/items?limit=10", nil)
	rec := httptest.NewRecorder()
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	bound, err := ParseQueryWithDefaults(ctx, query{Format: "csv", Limit: 50})
	if err != nil {
		t.Fatalf("ParseQueryWithDefaults returned error: %v", err)
	}
	if bound.Format != "csv" {
		t.Fatalf("expected default format to be preserved, got %+v", bound)
	}
	if bound.Limit != 10 {
		t.Fatalf("expected provided limit to override default, got %+v", bound)
	}
}

func TestParseQuery_NilContextReturnsInvalidInput(t *testing.T) {
	_, err := ParseQuery[struct {
		Limit int `query:"limit"`
	}](nil)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected invalid input, got %v", err)
	}
}

func TestParseQueryWithDefaults_NilContextReturnsInvalidInput(t *testing.T) {
	_, err := ParseQueryWithDefaults(nil, struct {
		Format string `query:"format"`
	}{Format: "csv"})
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected invalid input, got %v", err)
	}
}
