package rest

import (
	"net/http/httptest"
	"strings"
	"testing"

	"gochen-runtime/http/nethttp"
	"gochen/codec/idcodec"
	"gochen/errors"
)

type testPathID int64

func TestParsePathWithCodec(t *testing.T) {
	t.Run("custom int64 type", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, "")
		ctx.SetParam("id", "42")

		id, err := ParsePathWithCodec(ctx, "id", idcodec.NewInt64[testPathID](), "invalid id")
		if err != nil {
			t.Fatalf("ParsePathWithCodec returned error: %v", err)
		}
		if id != testPathID(42) {
			t.Fatalf("expected 42, got %d", id)
		}
	})

	t.Run("custom string type", func(t *testing.T) {
		type stringID string
		ctx := newPathHelperTestContext(t, "")
		ctx.SetParam("slug", "demo")

		id, err := ParsePathWithCodec(ctx, "slug", idcodec.NewString[stringID](), "invalid slug")
		if err != nil {
			t.Fatalf("ParsePathWithCodec returned error: %v", err)
		}
		if id != stringID("demo") {
			t.Fatalf("expected demo, got %q", id)
		}
	})

	t.Run("nil codec", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, "")
		ctx.SetParam("id", "42")

		_, err := ParsePathWithCodec[int64](ctx, "id", nil, "invalid id")
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("expected INVALID_INPUT, got %v", err)
		}
	})
}

func TestParsePath(t *testing.T) {
	t.Run("default int64 codec", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, "")
		ctx.SetParam("id", "42")

		id, err := ParsePath[int64](ctx, "id", "invalid id")
		if err != nil {
			t.Fatalf("ParsePath returned error: %v", err)
		}
		if id != 42 {
			t.Fatalf("expected 42, got %d", id)
		}
	})

	t.Run("invalid int64", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, "")
		ctx.SetParam("id", "abc")

		_, err := ParsePath[int64](ctx, "id", "invalid id")
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("expected INVALID_INPUT, got %v", err)
		}
		if err == nil || !strings.Contains(err.Error(), "invalid id") {
			t.Fatalf("expected invalid message, got %v", err)
		}
	})

	t.Run("default string codec", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, "")
		ctx.SetParam("slug", "demo")

		id, err := ParsePath[string](ctx, "slug", "invalid slug")
		if err != nil {
			t.Fatalf("ParsePath returned error: %v", err)
		}
		if id != "demo" {
			t.Fatalf("expected demo, got %q", id)
		}
	})

	t.Run("unsupported default codec", func(t *testing.T) {
		type customID struct{ Value int64 }
		ctx := newPathHelperTestContext(t, "")
		ctx.SetParam("id", "42")

		_, err := ParsePath[customID](ctx, "id", "invalid id")
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("expected INVALID_INPUT, got %v", err)
		}
	})
}

func TestBindOptionalJSON(t *testing.T) {
	t.Run("empty body", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, "")
		req := struct {
			Name string `json:"name"`
		}{}

		if err := BindOptionalJSON(ctx, &req); err != nil {
			t.Fatalf("BindOptionalJSON returned error: %v", err)
		}
		if req.Name != "" {
			t.Fatalf("expected zero value request, got %+v", req)
		}
	})

	t.Run("whitespace body", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, "   \n\t  ")
		req := struct {
			Name string `json:"name"`
		}{}

		if err := BindOptionalJSON(ctx, &req); err != nil {
			t.Fatalf("BindOptionalJSON returned error: %v", err)
		}
		if req.Name != "" {
			t.Fatalf("expected zero value request, got %+v", req)
		}
	})

	t.Run("valid json", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, `{"name":"demo"}`)
		req := struct {
			Name string `json:"name"`
		}{}

		if err := BindOptionalJSON(ctx, &req); err != nil {
			t.Fatalf("BindOptionalJSON returned error: %v", err)
		}
		if req.Name != "demo" {
			t.Fatalf("expected decoded request, got %+v", req)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, `{"name":`)
		req := struct {
			Name string `json:"name"`
		}{}

		err := BindOptionalJSON(ctx, &req)
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("expected INVALID_INPUT, got %v", err)
		}
	})
}

func TestBindJSONBodyFields(t *testing.T) {
	t.Run("binds dto and preserves field presence", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, `{"name":"demo","parent_id":null}`)
		req := struct {
			Name     string `json:"name"`
			ParentID *int64 `json:"parent_id"`
		}{}

		fields, err := BindJSONBodyFields(ctx, &req)
		if err != nil {
			t.Fatalf("BindJSONBodyFields returned error: %v", err)
		}
		if req.Name != "demo" {
			t.Fatalf("expected decoded request, got %+v", req)
		}
		if req.ParentID != nil {
			t.Fatalf("expected parent_id to decode as nil, got %v", req.ParentID)
		}
		if !fields.Has("parent_id") {
			t.Fatalf("expected parent_id field to be recorded as present")
		}
		if !fields.Has("name") {
			t.Fatalf("expected name field to be recorded as present")
		}
		if fields.Has("missing") {
			t.Fatalf("did not expect missing field to be present")
		}
	})

	t.Run("empty body", func(t *testing.T) {
		ctx := newPathHelperTestContext(t, " \n ")
		req := struct {
			Name string `json:"name"`
		}{}

		_, err := BindJSONBodyFields(ctx, &req)
		if !errors.Is(err, errors.InvalidInput) {
			t.Fatalf("expected INVALID_INPUT, got %v", err)
		}
	})
}

func newPathHelperTestContext(t *testing.T, body string) *nethttp.Context {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/items", strings.NewReader(body))
	ctx, err := nethttp.NewBaseContext(rec, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	return ctx
}
