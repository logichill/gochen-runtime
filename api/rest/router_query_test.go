package rest

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gochen-runtime/api/rest/internal/testutil"
	"gochen-runtime/http/middleware"
	"gochen-runtime/http/nethttp"
	appaudited "gochen/app/audited"
	"gochen/app/query"
	"gochen/domain/audited"
	"gochen/errors"
	"gochen/httpx"
)

type recordingAuditStoreArgs struct {
	lastEntityID string
	lastOffset   int
	lastLimit    int
}

// SaveAuditRecord 写入一条审计记录。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：audited.AuditRecord）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (s *recordingAuditStoreArgs) SaveAuditRecord(context.Context, audited.AuditRecord) (int64, error) {
	return 0, nil
}

// ListAuditRecordsByEntity 从存储中查询数据。
//
// 参数：
// - _：上下文（用于取消、超时与链路信息）
// - entityID：对象/实体标识
// - offset：分页偏移量（从 0 开始）
// - limit：分页大小（最大返回条数）
//
// 返回：
// - result1：列表结果（元素类型：audited.AuditRecord）
// - err：错误信息（nil 表示成功）
func (s *recordingAuditStoreArgs) ListAuditRecordsByEntity(_ context.Context, entityID string, offset, limit int) ([]audited.AuditRecord, error) {
	s.lastEntityID = entityID
	s.lastOffset = offset
	s.lastLimit = limit
	return []audited.AuditRecord{}, nil
}

// TestRouteBuilder_MaxBodySizeEnforced 验证 RouteBuilder MaxBodySizeEnforced。
func TestRouteBuilder_MaxBodySizeEnforced(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Body.MaxBodySize = 8
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["POST /items"]
	if !ok {
		t.Fatalf("expected POST handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/items", strings.NewReader(`{"id":123,"x":"yyyyyyyyyyyy"}`))
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413, got %d", w.Code)
	}
}

func TestRouteBuilder_MaxBodySizeNegativeUsesDefaultLimit(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Body.MaxBodySize = -1
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["POST /items"]
	if !ok {
		t.Fatalf("expected POST handler for /items")
	}

	payload := append([]byte(`{"id":123,"name":"`), bytes.Repeat([]byte("x"), 11<<20)...)
	payload = append(payload, []byte(`"}`)...)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/items", bytes.NewReader(payload))
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413 with MaxBodySize<0 default limit, got %d body %q", w.Code, w.Body.String())
	}
}

func TestRouteBuilder_WithCORSWritesRouteHeaders(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, WithCORS[*fakeEntity, int64]([]string{"https://example.test"}))
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items", nil)
	r.Header.Set("Origin", "https://example.test")
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://example.test" {
		t.Fatalf("expected CORS allow origin header, got %q", got)
	}

	optionsHandler, ok := group.Handlers["OPTIONS /items"]
	if !ok {
		t.Fatalf("expected OPTIONS handler for /items")
	}
	optionsW := httptest.NewRecorder()
	optionsReq := httptest.NewRequest("OPTIONS", "/items", nil)
	optionsReq.Header.Set("Origin", "https://example.test")
	optionsReq.Header.Set("Access-Control-Request-Method", "POST")
	optionsCtx, err := nethttp.NewBaseContext(optionsW, optionsReq)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := optionsHandler(optionsCtx); err != nil {
		t.Fatalf("options handler returned error: %v", err)
	}
	if optionsW.Code != http.StatusNoContent {
		t.Fatalf("expected CORS preflight status 204, got %d", optionsW.Code)
	}
	if got := optionsW.Header().Get("Access-Control-Allow-Origin"); got != "https://example.test" {
		t.Fatalf("expected preflight CORS allow origin header, got %q", got)
	}
	if got := optionsW.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, PATCH, OPTIONS" {
		t.Fatalf("expected CORS allow methods header, got %q", got)
	}
	if got := optionsW.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, Origin, X-CSRF-Token" {
		t.Fatalf("expected default CORS allow headers, got %q", got)
	}

	plainOptionsW := httptest.NewRecorder()
	plainOptionsReq := httptest.NewRequest("OPTIONS", "/items", nil)
	plainOptionsCtx, err := nethttp.NewBaseContext(plainOptionsW, plainOptionsReq)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	if err := optionsHandler(plainOptionsCtx); err != nil {
		t.Fatalf("plain options handler returned error: %v", err)
	}
	if got := plainOptionsW.Header().Get("Allow"); got != "GET, POST, OPTIONS" {
		t.Fatalf("expected Allow header %q, got %q", "GET, POST, OPTIONS", got)
	}
	if got := plainOptionsW.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected plain options not to carry CORS allow origin, got %q", got)
	}
	if got := plainOptionsW.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Fatalf("expected plain options not to carry CORS allow methods, got %q", got)
	}
	if got := plainOptionsW.Header().Get("Access-Control-Allow-Headers"); got != "" {
		t.Fatalf("expected plain options not to carry CORS allow headers, got %q", got)
	}
}

func TestRouteBuilder_RegisterReuseDoesNotCarryCORSPreflightRoutes(t *testing.T) {
	svc := newStubAppService(nil)
	rb := NewRouteBuilder[*fakeEntity, int64](svc)

	firstConfig := DefaultRouteConfig[int64]()
	firstConfig.Routing.BasePath = "/first"
	firstConfig.HTTP.CORS = &middleware.CORSConfig{AllowOrigins: []string{"https://example.test"}}
	firstGroup := testutil.NewMockRouteGroup()
	if err := rb.WithConfig(firstConfig).Register(firstGroup); err != nil {
		t.Fatalf("first register failed: %v", err)
	}
	if _, ok := firstGroup.Handlers["OPTIONS /first"]; !ok {
		t.Fatalf("expected first OPTIONS route")
	}

	secondConfig := DefaultRouteConfig[int64]()
	secondConfig.Routing.BasePath = "/second"
	secondConfig.HTTP.CORS = &middleware.CORSConfig{AllowOrigins: []string{"https://example.test"}}
	secondGroup := testutil.NewMockRouteGroup()
	if err := rb.WithConfig(secondConfig).Register(secondGroup); err != nil {
		t.Fatalf("second register failed: %v", err)
	}
	if _, ok := secondGroup.Handlers["OPTIONS /second"]; !ok {
		t.Fatalf("expected second OPTIONS route")
	}
	if _, ok := secondGroup.Handlers["OPTIONS /first"]; ok {
		t.Fatalf("did not expect reused builder to carry first OPTIONS route")
	}
}

func TestRouteBuilder_DirectCORSConfigFillsDefaultMethodsAndHeaders(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.HTTP.CORS = &middleware.CORSConfig{AllowOrigins: []string{"https://example.test"}}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}
	optionsHandler, ok := group.Handlers["OPTIONS /items"]
	if !ok {
		t.Fatalf("expected OPTIONS handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("OPTIONS", "/items", nil)
	r.Header.Set("Origin", "https://example.test")
	r.Header.Set("Access-Control-Request-Method", "POST")
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	if err := optionsHandler(ctx); err != nil {
		t.Fatalf("options handler returned error: %v", err)
	}

	if got := w.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST, PUT, DELETE, PATCH, OPTIONS" {
		t.Fatalf("expected default CORS allow methods, got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type, Authorization, Origin, X-CSRF-Token" {
		t.Fatalf("expected default CORS allow headers, got %q", got)
	}
}

func TestRouteBuilder_DefaultCORSDoesNotOpenCrossOrigin(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items", nil)
	r.Header.Set("Origin", "https://example.test")
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected default CORS to be closed, got allow origin %q", got)
	}
}

func TestDefaultRouteConfig_CORSIsMutableButClosedByDefault(t *testing.T) {
	cfg := DefaultRouteConfig[int64]()
	if cfg.HTTP.CORS == nil {
		t.Fatal("expected default CORS config to be mutable")
	}
	cfg.HTTP.CORS.AllowOrigins = []string{"https://example.test"}
	if got := cfg.HTTP.CORS.AllowOrigins[0]; got != "https://example.test" {
		t.Fatalf("unexpected configured origin: %q", got)
	}

	closed := DefaultRouteConfig[int64]()
	if len(nonEmptyStrings(closed.HTTP.CORS.AllowOrigins)) != 0 {
		t.Fatalf("expected default CORS origins to stay closed, got %+v", closed.HTTP.CORS.AllowOrigins)
	}
}

func TestRouteBuilder_CORSRejectsWildcardOriginWithCredentials(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.HTTP.CORS = &middleware.CORSConfig{
			AllowOrigins:     []string{"*"},
			AllowCredentials: true,
		}
	})

	group := testutil.NewMockRouteGroup()
	err = builder.Build(group)
	if err == nil {
		t.Fatalf("expected invalid CORS config error")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput error, got %v", err)
	}
	if len(group.Handlers) != 0 {
		t.Fatalf("expected no routes registered on invalid CORS config, got %v", group.Handlers)
	}
}

func TestRouteBuilder_CORSWhitespaceOriginsStayClosed(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.HTTP.CORS = &middleware.CORSConfig{AllowOrigins: []string{"", "   "}}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if _, ok := group.Handlers["OPTIONS /items"]; ok {
		t.Fatalf("expected whitespace-only origins not to register preflight route")
	}
}

func TestRouteBuilder_CORSPreflightMethodsByPath_NilConfigReturnsEmpty(t *testing.T) {
	var rb RouteBuilder[*fakeEntity, int64]
	methods := rb.corsPreflightMethodsByPath()
	if len(methods) != 0 {
		t.Fatalf("expected empty methods for nil config, got %+v", methods)
	}
}

func TestRouteBuilder_AuditedCORSRegistersRestoreAndPurgePreflight(t *testing.T) {
	repo := auditedNoopRepo{}
	store := &recordingAuditStoreArgs{}
	app, err := appaudited.NewApplication(&repo, nil, nil, store)
	if err != nil {
		t.Fatalf("new audited app: %v", err)
	}

	builder, err := NewApiBuilder[*auditedFakeEntity, int64](app, WithCORS[*auditedFakeEntity, int64]([]string{"https://example.test"}))
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Audit.OperatorExtractor = func(httpx.IContext) (string, bool) { return "tester", true }
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	restoreHandler, ok := group.Handlers["OPTIONS /items/:id/restore"]
	if !ok {
		t.Fatal("expected handler for OPTIONS /items/:id/restore")
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/items/1/restore", nil)
	req.Header.Set("Origin", "https://example.test")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	ctx, err := nethttp.NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	if err := restoreHandler(ctx); err != nil {
		t.Fatalf("restore preflight handler returned error: %v", err)
	}
	if got := w.Header().Get("Allow"); got != "POST, OPTIONS" {
		t.Fatalf("restore Allow = %q, want %q", got, "POST, OPTIONS")
	}

	purgeRepo := &auditedUpdateRepo{}
	purgeApp, err := appaudited.NewApplication[*auditedUpdateEntity, int64](purgeRepo, nil, nil, noopAuditStore{})
	if err != nil {
		t.Fatalf("new audited purge app: %v", err)
	}
	purgeBuilder, err := NewApiBuilder[*auditedUpdateEntity, int64](purgeApp, WithCORS[*auditedUpdateEntity, int64]([]string{"https://example.test"}))
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	purgeBuilder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Audit.OperatorExtractor = func(httpx.IContext) (string, bool) { return "tester", true }
	})

	purgeGroup := testutil.NewMockRouteGroup()
	if err := purgeBuilder.Build(purgeGroup); err != nil {
		t.Fatalf("purge build failed: %v", err)
	}

	purgeHandler, ok := purgeGroup.Handlers["OPTIONS /items/:id/purge"]
	if !ok {
		t.Fatal("expected handler for OPTIONS /items/:id/purge")
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodOptions, "/items/1/purge", nil)
	req.Header.Set("Origin", "https://example.test")
	req.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	ctx, err = nethttp.NewBaseContext(w, req)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	if err := purgeHandler(ctx); err != nil {
		t.Fatalf("purge preflight handler returned error: %v", err)
	}
	if got := w.Header().Get("Allow"); got != "DELETE, OPTIONS" {
		t.Fatalf("purge Allow = %q, want %q", got, "DELETE, OPTIONS")
	}
}

// TestRouteBuilder_QueryAllowlistApplied 验证 RouteBuilder QueryAllowlistApplied。
func TestRouteBuilder_QueryAllowlistApplied(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.AllowedFilterFields = []string{"name", "status"}
		cfg.Query.AllowedSortFields = []string{"created_at"}
		cfg.Query.AllowedFields = []string{"id", "name"}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?filter=name:like:John&filter=status:eq:active&sorts=created_at:desc&fields=id,name", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	if svc.lastQuery == nil {
		t.Fatalf("expected lastQuery to be captured")
	}

	if len(svc.lastQuery.Filters) != 2 {
		t.Fatalf("expected 2 filter fields, got %d", len(svc.lastQuery.Filters))
	}
	if !hasFilter(svc.lastQuery.Filters, "name", query.FilterOpLike, "John") {
		t.Fatalf("expected filter name:like:John, got %+v", svc.lastQuery.Filters)
	}
	if !hasFilter(svc.lastQuery.Filters, "status", query.FilterOpEq, "active") {
		t.Fatalf("expected filter status:eq:active, got %+v", svc.lastQuery.Filters)
	}

	if len(svc.lastQuery.Sorts) != 1 {
		t.Fatalf("expected 1 sort, got %d: %+v", len(svc.lastQuery.Sorts), svc.lastQuery.Sorts)
	}
	if svc.lastQuery.Sorts[0].Field != "created_at" || svc.lastQuery.Sorts[0].Direction != query.DESC {
		t.Fatalf("expected sort created_at=desc, got %+v", svc.lastQuery.Sorts[0])
	}

	if strings.Join(svc.lastQuery.Fields, ",") != "id,name" {
		t.Fatalf("expected fields id,name, got %v", svc.lastQuery.Fields)
	}
}

// TestRouteBuilder_QueryMultiSortOrderPreserved 验证 RouteBuilder QueryMultiSortOrderPreserved。
func TestRouteBuilder_QueryMultiSortOrderPreserved(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.AllowedSortFields = []string{"created_at", "id"}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?sorts=created_at:desc,id:asc", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if svc.lastQuery == nil {
		t.Fatalf("expected lastQuery to be captured")
	}
	if len(svc.lastQuery.Sorts) != 2 {
		t.Fatalf("expected 2 sorts, got %d: %+v", len(svc.lastQuery.Sorts), svc.lastQuery.Sorts)
	}
	if svc.lastQuery.Sorts[0].Field != "created_at" || svc.lastQuery.Sorts[0].Direction != query.DESC {
		t.Fatalf("expected sort[0]=created_at:desc, got %+v", svc.lastQuery.Sorts[0])
	}
	if svc.lastQuery.Sorts[1].Field != "id" || svc.lastQuery.Sorts[1].Direction != query.ASC {
		t.Fatalf("expected sort[1]=id:asc, got %+v", svc.lastQuery.Sorts[1])
	}
}

// TestRouteBuilder_AuditTrailPaginationApplied 验证 RouteBuilder AuditTrailPaginationApplied。
func TestRouteBuilder_AuditTrailPaginationApplied(t *testing.T) {
	repo := auditedNoopRepo{}
	store := &recordingAuditStoreArgs{}
	app, err := appaudited.NewApplication(&repo, nil, nil, store)
	if err != nil {
		t.Fatalf("new audited app: %v", err)
	}

	builder, err := NewApiBuilder[*auditedFakeEntity, int64](app, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Audit.OperatorExtractor = func(httpx.IContext) (string, bool) { return "tester", true }
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items/:id/audit"]
	if !ok {
		t.Fatalf("expected GET handler for /items/:id/audit")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items/1/audit?page=2&size=20", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	ctx.SetParam("id", "1")

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if store.lastEntityID != "1" || store.lastOffset != 20 || store.lastLimit != 20 {
		t.Fatalf("unexpected audit args: entity_id=%q offset=%d limit=%d", store.lastEntityID, store.lastOffset, store.lastLimit)
	}
}

// TestRouteBuilder_Delete_RejectsPurgeQueryParam 验证 RouteBuilder Delete RejectsPurgeQueryParam。
func TestRouteBuilder_Delete_RejectsPurgeQueryParam(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["DELETE /items/:id"]
	if !ok {
		t.Fatalf("expected DELETE handler for /items/:id")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/items/1?purge=true", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	ctx.SetParam("id", "1")

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
	if svc.lastCtx != nil {
		t.Fatalf("expected service not to be called when purge query param is present")
	}
}

// TestRouteBuilder_QueryDisallowedFilterReturnsInvalidInput 验证 RouteBuilder QueryDisallowedFilterReturnsInvalidInput。
func TestRouteBuilder_QueryDisallowedFilterReturnsInvalidInput(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.AllowedFilterFields = []string{"name", "status"}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?filter=role:eq:admin", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
	if svc.lastQuery != nil {
		t.Fatalf("expected service not to be called on invalid filter")
	}
}

// TestRouteBuilder_PaginationOptionsAllowlistApplied 验证分页路由同样应用查询白名单。
func TestRouteBuilder_PaginationOptionsAllowlistApplied(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = true
		cfg.Query.AllowedFilterFields = []string{"name"}
		cfg.Query.AllowedSortFields = []string{"created_at"}
		cfg.Query.AllowedFields = []string{"id"}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?page=2&size=10&filter=name:like:John&sorts=created_at:desc&fields=id", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	if svc.lastPage == nil {
		t.Fatalf("expected lastPage to be captured")
	}
	if svc.lastPage.Page != 2 || svc.lastPage.Size != 10 {
		t.Fatalf("expected page=2 size=10, got page=%d size=%d", svc.lastPage.Page, svc.lastPage.Size)
	}
	if len(svc.lastPage.Filters) != 1 {
		t.Fatalf("expected 1 filter field, got %d", len(svc.lastPage.Filters))
	}
	if !hasFilter(svc.lastPage.Filters, "name", query.FilterOpLike, "John") {
		t.Fatalf("expected filter name:like:John, got %+v", svc.lastPage.Filters)
	}
	if len(svc.lastPage.Sorts) != 1 {
		t.Fatalf("expected 1 sort, got %d: %+v", len(svc.lastPage.Sorts), svc.lastPage.Sorts)
	}
	if svc.lastPage.Sorts[0].Field != "created_at" || svc.lastPage.Sorts[0].Direction != query.DESC {
		t.Fatalf("expected sort created_at=desc, got %+v", svc.lastPage.Sorts[0])
	}
	if strings.Join(svc.lastPage.Fields, ",") != "id" {
		t.Fatalf("expected fields id, got %v", svc.lastPage.Fields)
	}
}

func TestRouteBuilder_PaginationRejectsOffsetOverflow(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = true
	})
	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?page="+strconv.Itoa(math.MaxInt)+"&size=1000", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	if err := group.Handlers["GET /items"](ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
	if svc.lastPage != nil {
		t.Fatalf("expected service not to be called for overflowing pagination")
	}
}

func TestRouteBuilder_PaginationAcceptsPageSizeAlias(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = true
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?page=1&page_size=20", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body %q", w.Code, w.Body.String())
	}
	if svc.lastPage == nil || svc.lastPage.Size != 20 {
		t.Fatalf("expected page_size to be used as size, got %+v", svc.lastPage)
	}
}

func TestRouteBuilder_PaginationPrefersSizeOverPageSizeAlias(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = true
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?page=1&size=5&page_size=20", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body %q", w.Code, w.Body.String())
	}
	if svc.lastPage == nil || svc.lastPage.Size != 5 {
		t.Fatalf("expected size to win over page_size alias, got %+v", svc.lastPage)
	}
}

func TestRouteBuilder_PlainQueryIgnoresPageSize(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?page_size=20", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body %q", w.Code, w.Body.String())
	}
	if svc.lastQuery == nil {
		t.Fatalf("expected service to be called when page_size is ignored on plain query")
	}
}

// TestRouteBuilder_QueryDisallowedSortFieldReturnsInvalidInput 验证 RouteBuilder 对不在白名单的排序字段返回 400。
func TestRouteBuilder_QueryDisallowedSortFieldReturnsInvalidInput(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.AllowedSortFields = []string{"created_at"}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?sorts=role:asc", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
	if svc.lastQuery != nil {
		t.Fatalf("expected service not to be called on invalid sort field")
	}
}

// TestRouteBuilder_QueryDisallowedFieldsReturnsInvalidInput 验证 RouteBuilder QueryDisallowedFieldsReturnsInvalidInput。
func TestRouteBuilder_QueryDisallowedFieldsReturnsInvalidInput(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.AllowedFields = []string{"id"}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?fields=id,name", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
	if svc.lastQuery != nil {
		t.Fatalf("expected service not to be called on invalid fields")
	}
}

// TestRouteBuilder_QueryInvalidSortDirectionReturnsInvalidInput 验证 RouteBuilder 对无效排序方向返回 400。
func TestRouteBuilder_QueryInvalidSortDirectionReturnsInvalidInput(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.AllowedSortFields = []string{"created_at"}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?sorts=created_at:down", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
	if svc.lastQuery != nil {
		t.Fatalf("expected service not to be called on invalid sort direction")
	}
}

// TestRouteBuilder_QuerySchemaNormalizesTypedFilters 验证 RouteBuilder QuerySchemaNormalizesTypedFilters。
func TestRouteBuilder_QuerySchemaNormalizesTypedFilters(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.QuerySchema = query.NewQuerySchema(
			query.BoolField("active"),
			query.TimeField("created_at", query.AllowSort()),
			query.StringField("id", query.AllowSelect()),
		)
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?filter=active:eq:TRUE&filter=created_at:gte:2026-03-27T10:00:00Z&sorts=created_at:desc&fields=id", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if svc.lastQuery == nil {
		t.Fatalf("expected lastQuery to be captured")
	}
	if !hasFilter(svc.lastQuery.Filters, "active", query.FilterOpEq, "true") {
		t.Fatalf("expected bool filter to normalize to true, got %+v", svc.lastQuery.Filters)
	}
	if !hasFilter(svc.lastQuery.Filters, "created_at", query.FilterOpGte, "2026-03-27T10:00:00Z") {
		t.Fatalf("expected time filter to stay normalized RFC3339, got %+v", svc.lastQuery.Filters)
	}
	if len(svc.lastQuery.Filters) != 2 {
		t.Fatalf("expected 2 filter fields, got %+v", svc.lastQuery.Filters)
	}
	activeFilters := svc.lastQuery.Filters.Get("active")
	if len(activeFilters) != 1 || !activeFilters[0].Value.Bool {
		t.Fatalf("expected decoded bool filter to be true, got %+v", activeFilters)
	}
	createdFilters := svc.lastQuery.Filters.Get("created_at")
	if len(createdFilters) != 1 || createdFilters[0].Value.Time.IsZero() {
		t.Fatalf("expected decoded time filter to be populated, got %+v", createdFilters)
	}
}

// TestRouteBuilder_PaginationQuerySchemaDecodesTypedFilters 验证分页模式同样解码 typed filter。
func TestRouteBuilder_PaginationQuerySchemaDecodesTypedFilters(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = true
		cfg.Query.QuerySchema = query.NewQuerySchema(
			query.BoolField("active"),
		)
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?page=1&size=10&filter=active:eq:true", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
	if svc.lastPage == nil {
		t.Fatalf("expected pagination options to be captured")
	}
	activeFilters := svc.lastPage.Filters.Get("active")
	if len(activeFilters) != 1 || !activeFilters[0].Value.Bool {
		t.Fatalf("expected decoded pagination filter to be true, got %+v", activeFilters)
	}
}

// TestRouteBuilder_QuerySchemaRejectsInvalidTypedFilter 验证 RouteBuilder QuerySchemaRejectsInvalidTypedFilter。
func TestRouteBuilder_QuerySchemaRejectsInvalidTypedFilter(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.QuerySchema = query.NewQuerySchema(
			query.BoolField("active"),
		)
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?filter=active:like:TRUE", nil)
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
	if svc.lastQuery != nil {
		t.Fatalf("expected service not to be called on invalid typed filter")
	}
}

// hasFilter 执行对应操作。
//
// 参数：
// - field：参数值（具体语义见函数上下文）（类型：string）
// - op：操作对象（类型：query.FilterOp）
// - value：值（待写入/比较/校验的值）（类型：string）
//
// 返回：
// - result：是否满足条件
func hasFilter(filters query.QueryFilters, field string, op query.FilterOp, value string) bool {
	for _, f := range filters.Get(field) {
		if f.Op == op && f.Value.Normalized == value {
			return true
		}
	}
	return false
}
