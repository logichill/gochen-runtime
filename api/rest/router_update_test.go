package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gochen-runtime/api/rest/internal/testutil"
	"gochen-runtime/http/nethttp"
	"gochen-runtime/internal/httpheader"
	appaudited "gochen/app/audited"
	appcrud "gochen/app/crud"
	"gochen/contextx"
	"gochen/db/tx"
	"gochen/domain/audited"
	"gochen/domain/crud"
	"gochen/errors"
	"gochen/httpx"
)

func TestBuildAllowHeaderDoesNotMutateInput(t *testing.T) {
	methods := map[string]struct{}{
		http.MethodGet:  {},
		http.MethodPost: {},
	}

	got := httpheader.BuildAllowHeader(methods)

	if got != "GET, POST, OPTIONS" {
		t.Fatalf("unexpected Allow header: %q", got)
	}
	if _, ok := methods[http.MethodOptions]; ok {
		t.Fatalf("buildAllowHeader mutated input map")
	}
	if len(methods) != 2 {
		t.Fatalf("expected original map size 2, got %d", len(methods))
	}
}

// TestRouteBuilder_Update_RejectsMismatchedBodyID 验证 RouteBuilder Update RejectsMismatchedBodyID。
func TestRouteBuilder_Update_RejectsMismatchedBodyID(t *testing.T) {
	repo := &capturingRepository{}
	svc, err := appcrud.NewApplication[*fakeEntity, int64](repo, nil, nil)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}

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
	handler := group.Handlers["PUT /items/:id"]

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/items/1", strings.NewReader(`{"id":2,"name":"hacked"}`))
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
	if repo.updateCalls != 0 {
		t.Fatalf("expected Update not called on mismatched id, got %d", repo.updateCalls)
	}
}

// TestRouteBuilder_Update_KeepsPathIDWhenBodyOmitsID 验证 RouteBuilder Update KeepsPathIDWhenBodyOmitsID。
func TestRouteBuilder_Update_KeepsPathIDWhenBodyOmitsID(t *testing.T) {
	repo := &capturingRepository{}
	svc, err := appcrud.NewApplication[*fakeEntity, int64](repo, nil, nil)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}

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
	handler := group.Handlers["PUT /items/:id"]

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/items/1", strings.NewReader(`{"name":"ok"}`))
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
	if repo.updateCalls != 1 {
		t.Fatalf("expected Update called once, got %d", repo.updateCalls)
	}
	if repo.gotUpdate == nil || repo.gotUpdate.ID != 1 {
		t.Fatalf("expected updated entity to keep path id=1, got %+v", repo.gotUpdate)
	}
}

type auditedUpdateEntity struct {
	*audited.AuditedEntity[int64]
	Name string `json:"name,omitempty"`
}

type auditedUpdateRepo struct {
	updateCalls int
	lastUpdate  *auditedUpdateEntity
}

// BeginTx 开启事务。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
//
// 返回：
// - result1：返回结果（类型：context.Context）
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) BeginTx(ctx context.Context) (tx.TxScope, error) {
	return tx.NewTxScope(ctx, true)
}

func (r *auditedUpdateRepo) WithinTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	txScope, err := r.BeginTx(ctx)
	if err != nil {
		return err
	}
	txCtx := txScope.Context()
	if txCtx == nil {
		return errors.NewCode(errors.Internal, "BeginTx returned nil txCtx")
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = r.Rollback(txScope)
	}()
	if err := fn(txCtx); err != nil {
		return err
	}
	if err := r.Commit(txScope); err != nil {
		if contextx.IsAfterCommitError(err) {
			committed = true
		}
		return err
	}
	committed = true
	return contextx.RunAfterCommit(txCtx)
}

// Commit 提交事务。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Commit(tx.TxScope) error { return nil }

// Rollback 回滚事务。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Rollback(tx.TxScope) error { return nil }

// Create 创建对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - entity：参数值（具体语义见函数上下文）（类型：*auditedUpdateEntity）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Create(context.Context, *auditedUpdateEntity) error { return nil }

// Update 更新对象并写入存储。
//
// 参数：
// - _：上下文（用于取消、超时与链路信息）
// - e：要更新的实体
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Update(_ context.Context, e *auditedUpdateEntity) error {
	r.updateCalls++
	r.lastUpdate = e
	return nil
}

// Delete 删除实体并同步到存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Delete(context.Context, int64) error { return nil }

// Purge context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Purge(context.Context, int64) error { return nil }

// Get 从存储中查询实体。
//
// 参数：
// - _：上下文（用于取消、超时与链路信息）
// - id：对象/实体标识
//
// 返回：
// - result1：返回结果（类型：*auditedUpdateEntity）
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Get(_ context.Context, id int64) (*auditedUpdateEntity, error) {
	return &auditedUpdateEntity{
		AuditedEntity: &audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: id, Version: 7}},
		Name:          "existing",
	}, nil
}

// GetWithDeleted 从存储中查询实体。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - id：对象/实体标识
//
// 返回：
// - result1：返回结果（类型：*auditedUpdateEntity）
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) GetWithDeleted(ctx context.Context, id int64) (*auditedUpdateEntity, error) {
	return r.Get(ctx, id)
}

// List 从存储中查询实体。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int）
// - value：参数值（具体语义见函数上下文）（类型：int）
//
// 返回：
// - result1：列表结果（元素类型：*auditedUpdateEntity）
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) List(context.Context, int, int) ([]*auditedUpdateEntity, error) {
	return []*auditedUpdateEntity{}, nil
}

// ListDeleted 从存储中查询实体。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int）
// - value：参数值（具体语义见函数上下文）（类型：int）
//
// 返回：
// - result1：列表结果（元素类型：*auditedUpdateEntity）
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) ListDeleted(context.Context, int, int) ([]*auditedUpdateEntity, error) {
	return []*auditedUpdateEntity{}, nil
}

// Count 返回匹配条件的记录数。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Count(context.Context) (int64, error) { return 0, nil }

// Exists 判断对象是否存在。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - result1：是否满足条件
// - err：错误信息（nil 表示成功）
func (r *auditedUpdateRepo) Exists(context.Context, int64) (bool, error) { return true, nil }

type noopAuditStore struct{}

// SaveAuditRecord 写入一条审计记录。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：audited.AuditRecord）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (noopAuditStore) SaveAuditRecord(context.Context, audited.AuditRecord) (int64, error) {
	return 0, nil
}

// ListAuditRecordsByEntity 从存储中查询数据。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - s：参数值（具体语义见函数上下文）（类型：string）
// - value：参数值（具体语义见函数上下文）（类型：int）
// - value：参数值（具体语义见函数上下文）（类型：int）
//
// 返回：
// - result1：列表结果（元素类型：audited.AuditRecord）
// - err：错误信息（nil 表示成功）
func (noopAuditStore) ListAuditRecordsByEntity(context.Context, string, int, int) ([]audited.AuditRecord, error) {
	return nil, nil
}

// TestRouteBuilder_AuditedUpdate_RequiresVersion 验证 RouteBuilder AuditedUpdate RequiresVersion。
func TestRouteBuilder_AuditedUpdate_RequiresVersion(t *testing.T) {
	repo := &auditedUpdateRepo{}
	app, err := appaudited.NewApplication[*auditedUpdateEntity, int64](repo, nil, nil, noopAuditStore{})
	if err != nil {
		t.Fatalf("new audited app: %v", err)
	}

	builder, err := NewApiBuilder[*auditedUpdateEntity, int64](app, nil)
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
	handler := group.Handlers["PUT /items/:id"]

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/items/1", strings.NewReader(`{"name":"ok"}`))
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
	if repo.updateCalls != 0 {
		t.Fatalf("expected Update not called when version missing, got %d", repo.updateCalls)
	}
}

// TestRouteBuilder_AuditedUpdate_RejectsMismatchedVersion 验证 RouteBuilder AuditedUpdate RejectsMismatchedVersion。
func TestRouteBuilder_AuditedUpdate_RejectsMismatchedVersion(t *testing.T) {
	repo := &auditedUpdateRepo{}
	app, err := appaudited.NewApplication[*auditedUpdateEntity, int64](repo, nil, nil, noopAuditStore{})
	if err != nil {
		t.Fatalf("new audited app: %v", err)
	}

	builder, err := NewApiBuilder[*auditedUpdateEntity, int64](app, nil)
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
	handler := group.Handlers["PUT /items/:id"]

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/items/1", strings.NewReader(`{"version":6,"name":"ok"}`))
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}
	ctx.SetParam("id", "1")

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d", w.Code)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("expected Update not called on version mismatch, got %d", repo.updateCalls)
	}
}

// TestRouteBuilder_AuditedUpdate_RejectsAuditFields 验证 RouteBuilder AuditedUpdate RejectsAuditFields。
func TestRouteBuilder_AuditedUpdate_RejectsAuditFields(t *testing.T) {
	repo := &auditedUpdateRepo{}
	app, err := appaudited.NewApplication[*auditedUpdateEntity, int64](repo, nil, nil, noopAuditStore{})
	if err != nil {
		t.Fatalf("new audited app: %v", err)
	}

	builder, err := NewApiBuilder[*auditedUpdateEntity, int64](app, nil)
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
	handler := group.Handlers["PUT /items/:id"]

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/items/1", strings.NewReader(`{"version":7,"DELETED_AT":"2026-01-01T00:00:00Z"}`))
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
	if repo.updateCalls != 0 {
		t.Fatalf("expected Update not called when audit fields present, got %d", repo.updateCalls)
	}
}

// TestRouteBuilder_AuditedUpdate_SucceedsWithMatchingVersion 验证 RouteBuilder AuditedUpdate SucceedsWithMatchingVersion。
func TestRouteBuilder_AuditedUpdate_SucceedsWithMatchingVersion(t *testing.T) {
	repo := &auditedUpdateRepo{}
	app, err := appaudited.NewApplication[*auditedUpdateEntity, int64](repo, nil, nil, noopAuditStore{})
	if err != nil {
		t.Fatalf("new audited app: %v", err)
	}

	builder, err := NewApiBuilder[*auditedUpdateEntity, int64](app, nil)
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
	handler := group.Handlers["PUT /items/:id"]

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/items/1", strings.NewReader(`{"version":7,"name":"ok"}`))
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
	if repo.updateCalls != 1 {
		t.Fatalf("expected Update called once, got %d", repo.updateCalls)
	}
	if repo.lastUpdate == nil || repo.lastUpdate.Name != "ok" {
		t.Fatalf("unexpected updated entity: %+v", repo.lastUpdate)
	}
	if repo.lastUpdate.GetVersion() != 7 {
		t.Fatalf("expected version=7, got %d", repo.lastUpdate.GetVersion())
	}
}
