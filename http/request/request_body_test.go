package request

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gochen/errors"
	"gochen/httpx"
)

func TestBindJSON_StrictRejectsTrailingData(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	tests := []struct {
		name string
		body string
	}{
		{name: "trailing data", body: `{"name":"alice"} {"name":"bob"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			err := BindJSON(httptest.NewRecorder(), req, map[string]any{}, "body", &payload{})
			if !errors.Is(err, errors.InvalidInput) {
				t.Fatalf("BindJSON error = %v, want InvalidInput", err)
			}
		})
	}
}

func TestBindJSON_StrictRejectsUnknownFields(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"alice","nmae":"typo"}`))
	err := BindJSON(httptest.NewRecorder(), req, map[string]any{}, "body", &payload{})
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("BindJSON error = %v, want InvalidInput", err)
	}
}

func TestReadBody_CachesForRepeatedReads(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"alice"}`))
	values := map[string]any{}

	first, err := ReadBody(httptest.NewRecorder(), req, values, "body")
	if err != nil {
		t.Fatalf("first ReadBody error = %v", err)
	}
	req.Body = nil
	second, err := ReadBody(httptest.NewRecorder(), req, values, "body")
	if err != nil {
		t.Fatalf("second ReadBody error = %v", err)
	}
	if string(first) != `{"name":"alice"}` || string(second) != string(first) {
		t.Fatalf("cached body mismatch: first=%q second=%q", first, second)
	}
}

func TestReadBody_RejectsNilRequest(t *testing.T) {
	_, err := ReadBody(httptest.NewRecorder(), nil, nil, "body")
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("ReadBody error = %v, want InvalidInput", err)
	}
}

func TestReadBody_RejectsNilRequestBody(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"alice"}`))
	req.Body = nil

	_, err := ReadBody(httptest.NewRecorder(), req, nil, "body")
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("ReadBody error = %v, want InvalidInput", err)
	}
}

func TestBindJSON_RejectsNilRequest(t *testing.T) {
	var target struct {
		Name string `json:"name"`
	}

	err := BindJSON(httptest.NewRecorder(), nil, nil, "body", &target)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("BindJSON error = %v, want InvalidInput", err)
	}
}

func TestBindJSON_RejectsNilRequestBody(t *testing.T) {
	req := &http.Request{}
	var target struct {
		Name string `json:"name"`
	}

	err := BindJSON(httptest.NewRecorder(), req, nil, "body", &target)
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("BindJSON error = %v, want InvalidInput", err)
	}
}

func TestReadBody_RestoresBodyForDownstreamReaders(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"alice"}`))

	body, err := ReadBody(httptest.NewRecorder(), req, nil, "body")
	if err != nil {
		t.Fatalf("ReadBody error = %v", err)
	}
	if string(body) != `{"name":"alice"}` {
		t.Fatalf("ReadBody body = %q", body)
	}

	reread, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("re-read body error = %v", err)
	}
	if string(reread) != string(body) {
		t.Fatalf("re-read body = %q, want %q", reread, body)
	}
}

func TestReadBody_EnforcesMaxBytes(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader("12345"))
	values := map[string]any{
		httpx.MaxBodySizeKey: httpx.ValueOf[int64](4),
	}

	_, err := ReadBody(httptest.NewRecorder(), req, values, "body")
	if !errors.Is(err, errors.PayloadTooLarge) {
		t.Fatalf("ReadBody error = %v, want PayloadTooLarge", err)
	}
}

func TestReadBody_IntZeroUsesDefaultLimit(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("a", int(httpx.DefaultMaxBodySizeBytes)+1)))
	values := map[string]any{
		httpx.MaxBodySizeKey: httpx.ValueOf[int](0),
	}

	_, err := ReadBody(httptest.NewRecorder(), req, values, "body")
	if !errors.Is(err, errors.PayloadTooLarge) {
		t.Fatalf("ReadBody error = %v, want PayloadTooLarge", err)
	}
}

func TestReadBody_NegativeIntUsesDefaultLimit(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("a", int(httpx.DefaultMaxBodySizeBytes)+1)))
	values := map[string]any{
		httpx.MaxBodySizeKey: httpx.ValueOf[int](-1),
	}

	_, err := ReadBody(httptest.NewRecorder(), req, values, "body")
	if !errors.Is(err, errors.PayloadTooLarge) {
		t.Fatalf("ReadBody error = %v, want PayloadTooLarge", err)
	}
}

func TestReadBody_NegativeInt64UsesDefaultLimit(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("a", int(httpx.DefaultMaxBodySizeBytes)+1)))
	values := map[string]any{
		httpx.MaxBodySizeKey: httpx.ValueOf[int64](-1),
	}

	_, err := ReadBody(httptest.NewRecorder(), req, values, "body")
	if !errors.Is(err, errors.PayloadTooLarge) {
		t.Fatalf("ReadBody error = %v, want PayloadTooLarge", err)
	}
}

func TestReadBody_ZeroInt64UsesDefaultLimit(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("a", int(httpx.DefaultMaxBodySizeBytes)+1)))
	values := map[string]any{
		httpx.MaxBodySizeKey: httpx.ValueOf[int64](0),
	}

	_, err := ReadBody(httptest.NewRecorder(), req, values, "body")
	if !errors.Is(err, errors.PayloadTooLarge) {
		t.Fatalf("ReadBody error = %v, want PayloadTooLarge", err)
	}
}
