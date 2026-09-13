package rest

import (
	"context"
	"fmt"
	"gochen/errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"gochen-runtime/http/nethttp"
	"gochen-runtime/internal/testutil"
	appaudited "gochen/app/audited"
	appcrud "gochen/app/crud"
	"gochen/app/query"
	"gochen/contextx"
	"gochen/db/tx"
	"gochen/domain/audited"
	"gochen/domain/crud"
	"gochen/httpx"
	"gochen/validate"
)

func TestRouteBuilder_RegisterRejectsNilGroup(t *testing.T) {
	err := NewRouteBuilder[*fakeEntity, int64](newStubAppService(nil)).Register(nil)
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, errors.InvalidInput) {
		t.Fatalf("expected InvalidInput, got %v", err)
	}
}

// TestRestfulBuilderAppliesServiceConfig 验证 RestfulBuilderAppliesServiceConfig。
func TestRestfulBuilderAppliesServiceConfig(t *testing.T) {
	repo := &noopRepository{}
	validator := &trackingValidator{err: errors.New("validator hit")}

	svc, err := appcrud.NewApplication(repo, nil, nil)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, WithValidator[*fakeEntity, int64](validator))
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	if builder.routeConfig.Body.Validator != nil {
		t.Fatal("WithValidator must not configure HTTP body validation")
	}

	builder.Service(func(cfg *appcrud.ServiceConfig) {
		cfg.MaxBatchSize = 77
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	if got := svc.Config().MaxBatchSize; got != 77 {
		t.Fatalf("expected MaxBatchSize 77, got %d", got)
	}

	if err := svc.Validate(&fakeEntity{}); !errors.Is(err, validator.err) {
		t.Fatalf("expected validator error, got %v", err)
	}

	if validator.called != 1 {
		t.Fatalf("expected validator to be called once, got %d", validator.called)
	}
}

type applicationWithoutOptionalConfiguration struct {
	appcrud.IApplication[*fakeEntity, int64]
}

func TestApiBuilder_FailsWhenConfiguredCapabilityIsUnsupported(t *testing.T) {
	base := newStubAppService(nil)
	tests := []struct {
		name      string
		configure func(*ApiBuilder[*fakeEntity, int64])
	}{
		{
			name: "service config",
			configure: func(builder *ApiBuilder[*fakeEntity, int64]) {
				builder.Service(func(cfg *appcrud.ServiceConfig) { cfg.MaxBatchSize = 7 })
			},
		},
		{
			name: "validator",
			configure: func(builder *ApiBuilder[*fakeEntity, int64]) {
				builder.validator = &trackingValidator{}
			},
		},
		{
			name: "hooks",
			configure: func(builder *ApiBuilder[*fakeEntity, int64]) {
				builder.Hooks(func(hooks *appcrud.Hooks[*fakeEntity, int64]) {
					hooks.BeforeCreate = func(context.Context, *fakeEntity) error { return nil }
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &applicationWithoutOptionalConfiguration{IApplication: base}
			builder, err := NewApiBuilder[*fakeEntity, int64](service)
			if err != nil {
				t.Fatalf("NewApiBuilder returned error: %v", err)
			}
			tt.configure(builder)
			err = builder.Build(testutil.NewMockRouteGroup())
			if !errors.Is(err, errors.FailedPrecondition) {
				t.Fatalf("expected FailedPrecondition, got %v", err)
			}
		})
	}
}

func TestApiBuilder_AutoInfersQuerySchema(t *testing.T) {
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

	if builder.routeConfig.Query.QuerySchema == nil {
		t.Fatalf("expected query schema to be auto inferred")
	}
	if !reflect.DeepEqual(builder.routeConfig.Query.AllowedFilterFields, []string{"active", "created_at", "name"}) {
		t.Fatalf("unexpected allowed filter fields: %v", builder.routeConfig.Query.AllowedFilterFields)
	}
	if !reflect.DeepEqual(builder.routeConfig.Query.AllowedSortFields, []string{"active", "created_at", "id", "name"}) {
		t.Fatalf("unexpected allowed sort fields: %v", builder.routeConfig.Query.AllowedSortFields)
	}
	if !reflect.DeepEqual(builder.routeConfig.Query.AllowedFields, []string{"active", "created_at", "id", "name"}) {
		t.Fatalf("unexpected allowed fields: %v", builder.routeConfig.Query.AllowedFields)
	}

	handler, ok := group.Handlers["GET /items"]
	if !ok {
		t.Fatalf("expected GET handler for /items")
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/items?filter=active:eq:TRUE&sorts=created_at:desc&fields=id,name", nil)
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
	activeFilters := svc.lastQuery.Filters.Get("active")
	if len(activeFilters) != 1 || !activeFilters[0].Value.Bool {
		t.Fatalf("expected decoded bool filter, got %+v", activeFilters)
	}
}

func TestApiBuilder_ExplicitQuerySchemaWinsOverAutoInfer(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	manual := query.NewQuerySchema(
		query.StringField("custom_name", query.WithFilterOps(query.FilterOpLike)),
	)
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.QuerySchema = manual
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	if builder.routeConfig.Query.QuerySchema != manual {
		t.Fatalf("expected explicit query schema to be preserved")
	}
	if !reflect.DeepEqual(builder.routeConfig.Query.AllowedFilterFields, []string{"custom_name"}) {
		t.Fatalf("unexpected allowed filter fields: %v", builder.routeConfig.Query.AllowedFilterFields)
	}
	if len(builder.routeConfig.Query.AllowedSortFields) != 0 {
		t.Fatalf("expected explicit schema sort allowlist to stay empty, got %v", builder.routeConfig.Query.AllowedSortFields)
	}
	if len(builder.routeConfig.Query.AllowedFields) != 0 {
		t.Fatalf("expected explicit schema field allowlist to stay empty, got %v", builder.routeConfig.Query.AllowedFields)
	}
}

func TestApiBuilder_QuerySchemaInferOptionsForceInference(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Query.EnablePagination = false
		cfg.Query.AllowedFilterFields = []string{"active"}
		cfg.Query.QuerySchemaInferOptions = &query.SchemaInferOptions{
			FieldNameMapper: func(field reflect.StructField) string {
				return strings.ToLower(field.Name)
			},
		}
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	if builder.routeConfig.Query.QuerySchema == nil {
		t.Fatalf("expected query schema to be inferred when infer options are set")
	}
	if _, ok := builder.routeConfig.Query.QuerySchema.Field("createdat"); !ok {
		t.Fatalf("expected custom mapper field createdat to exist")
	}
	if !reflect.DeepEqual(builder.routeConfig.Query.AllowedFilterFields, []string{"active"}) {
		t.Fatalf("expected manual allowlist to be preserved, got %v", builder.routeConfig.Query.AllowedFilterFields)
	}
}

type auditedFakeEntity struct {
	*audited.AuditedEntity[int64]
}

type auditedNoopRepo struct{}

// Create 创建对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - entity：参数值（具体语义见函数上下文）（类型：*auditedFakeEntity）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) Create(context.Context, *auditedFakeEntity) error { return nil }

// Get 从存储中查询实体。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - result1：返回结果（类型：*auditedFakeEntity）
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) Get(context.Context, int64) (*auditedFakeEntity, error) { return nil, nil }

// GetWithDeleted 从存储中查询实体。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - result1：返回结果（类型：*auditedFakeEntity）
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) GetWithDeleted(context.Context, int64) (*auditedFakeEntity, error) {
	return nil, nil
}

// Update 更新对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - entity：参数值（具体语义见函数上下文）（类型：*auditedFakeEntity）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) Update(context.Context, *auditedFakeEntity) error { return nil }

// Delete 删除实体并同步到存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) Delete(context.Context, int64) error { return nil }

// List 从存储中查询实体。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int）
// - value：参数值（具体语义见函数上下文）（类型：int）
//
// 返回：
// - result1：列表结果（元素类型：*auditedFakeEntity）
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) List(context.Context, int, int) ([]*auditedFakeEntity, error) {
	return []*auditedFakeEntity{}, nil
}

// ListDeleted 从存储中查询实体。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int）
// - value：参数值（具体语义见函数上下文）（类型：int）
//
// 返回：
// - result1：列表结果（元素类型：*auditedFakeEntity）
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) ListDeleted(context.Context, int, int) ([]*auditedFakeEntity, error) {
	return []*auditedFakeEntity{}, nil
}

// Count 返回匹配条件的记录数。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) Count(context.Context) (int64, error) { return 0, nil }

// Exists 判断对象是否存在。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - result1：是否满足条件
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) Exists(context.Context, int64) (bool, error) { return true, nil }

// BeginTx 开启事务。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
//
// 返回：
// - result1：返回结果（类型：context.Context）
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) BeginTx(ctx context.Context) (tx.TxScope, error) {
	return tx.NewTxScope(ctx, true)
}

func (r auditedNoopRepo) WithinTx(ctx context.Context, fn func(txCtx context.Context) error) error {
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
func (auditedNoopRepo) Commit(tx.TxScope) error { return nil }

// Rollback 回滚事务。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (auditedNoopRepo) Rollback(tx.TxScope) error { return nil }

type stubAuditStore struct{}

// SaveAuditRecord 写入一条审计记录。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：audited.AuditRecord）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (stubAuditStore) SaveAuditRecord(context.Context, audited.AuditRecord) (int64, error) {
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
func (stubAuditStore) ListAuditRecordsByEntity(context.Context, string, int, int) ([]audited.AuditRecord, error) {
	return nil, nil
}

// TestApiBuilder_FailsFastWhenEntityIsAuditedButServiceNotAudited 验证 ApiBuilder FailsFastWhenEntityIsAuditedButServiceNotAudited。
func TestApiBuilder_FailsFastWhenEntityIsAuditedButServiceNotAudited(t *testing.T) {
	repo := auditedNoopRepo{}
	svc, err := appcrud.NewApplication(&repo, nil, nil)
	if err != nil {
		t.Fatalf("new application: %v", err)
	}
	builder, err := NewApiBuilder[*auditedFakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	group := testutil.NewMockRouteGroup()

	buildErr := builder.Build(group)
	if buildErr == nil {
		t.Fatalf("expected build to fail when audited entity is used with non-audited service")
	}
}

// TestApiBuilder_RegistersAuditedRoutesWhenEntityIsAudited 验证 ApiBuilder RegistersAuditedRoutesWhenEntityIsAudited。
func TestApiBuilder_RegistersAuditedRoutesWhenEntityIsAudited(t *testing.T) {
	repo := auditedNoopRepo{}
	auditedApp, err := appaudited.NewApplication(&repo, nil, nil, stubAuditStore{})
	if err != nil {
		t.Fatalf("new audited app: %v", err)
	}
	builder, err := NewApiBuilder[*auditedFakeEntity, int64](auditedApp, nil)
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
	if _, ok := group.Handlers["GET /items/:id/audit"]; !ok {
		t.Fatalf("expected GET /items/:id/audit")
	}
	if _, ok := group.Handlers["POST /items/:id/restore"]; !ok {
		t.Fatalf("expected POST /items/:id/restore")
	}
	if _, ok := group.Handlers["GET /items/deleted"]; !ok {
		t.Fatalf("expected GET /items/deleted")
	}
	if _, ok := group.Handlers["DELETE /items/:id/purge"]; ok {
		t.Fatalf("did not expect DELETE /items/:id/purge without IPurgeRepository support")
	}
}

type stubAppService struct {
	cfg       *appcrud.ServiceConfig
	validator validate.IValidator
	order     *[]string
	lastQuery *query.QueryRequest
	lastPage  *query.PaginationOptions
	lastCtx   context.Context

	createGuardedCalls      int
	updateGuardedCalls      int
	deleteGuardedCalls      int
	createBatchGuardedCalls int
	updateBatchGuardedCalls int
	deleteBatchGuardedCalls int
	createAllCalls          int
	updateAllCalls          int
	deleteAllCalls          int
	lastCreated             *fakeEntity
	lastUpdated             *fakeEntity
	lastDeletedID           int64
	loadedEntity            *fakeEntity
	repository              crud.IRepository[*fakeEntity, int64]
}

func newStubAppService(order *[]string) *stubAppService {
	return &stubAppService{
		cfg:   cloneServiceConfig(appcrud.DefaultServiceConfig()),
		order: order,
	}
}

// Create 创建实体（测试 stub，记录 ctx 用于断言）。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *stubAppService) Create(ctx context.Context, entity *fakeEntity) error {
	s.lastCtx = ctx
	s.lastCreated = entity
	return nil
}

// Get 返回当前值。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - result1：返回结果（类型：*fakeEntity）
// - err：错误信息（nil 表示成功）
func (s *stubAppService) Get(ctx context.Context, id int64) (*fakeEntity, error) {
	s.lastCtx = ctx
	if s.loadedEntity != nil {
		entity := *s.loadedEntity
		if entity.ID == 0 {
			entity.ID = id
		}
		return &entity, nil
	}
	return &fakeEntity{ID: id, Version: 7, Name: "existing"}, nil
}

// Update ctx：上下文（用于取消、超时与链路信息）。
//
// 参数：
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *stubAppService) Update(ctx context.Context, _ *fakeEntity) error {
	s.lastCtx = ctx
	return nil
}

// Delete 删除数据。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *stubAppService) Delete(ctx context.Context, _ int64) error { s.lastCtx = ctx; return nil }

// List context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - value：参数值（具体语义见函数上下文）（类型：int）
// - value：参数值（具体语义见函数上下文）（类型：int）
//
// 返回：
// - result1：列表结果（元素类型：*fakeEntity）
// - err：错误信息（nil 表示成功）
func (s *stubAppService) List(context.Context, int, int) ([]*fakeEntity, error) {
	return []*fakeEntity{}, nil
}

// Count 返回匹配条件的记录数。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (s *stubAppService) Count(context.Context) (int64, error) { return 0, nil }

// Exists 判断对象是否存在。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - value：参数值（具体语义见函数上下文）（类型：int64）
//
// 返回：
// - result1：是否满足条件
// - err：错误信息（nil 表示成功）
func (s *stubAppService) Exists(context.Context, int64) (bool, error) { return true, nil }

// Repository result：测试返回值（类型：crud.IRepository[*fakeEntity, int64]）。
//
// 返回：
func (s *stubAppService) Repository() crud.IRepository[*fakeEntity, int64] { return s.repository }

func (s *stubAppService) QueryRepository() (crud.IQueryRepository[*fakeEntity, int64], bool) {
	return nil, false
}

// ListByQuery _：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - query：参数值（具体语义见函数上下文）（类型：*query.QueryRequest）
//
// 返回：
// - result1：列表结果（元素类型：*fakeEntity）
// - err：错误信息（nil 表示成功）
func (s *stubAppService) ListByQuery(ctx context.Context, query *query.QueryRequest) ([]*fakeEntity, error) {
	// 记录调用参数，便于测试断言（测试用例不会在调用后再修改入参）。
	s.lastCtx = ctx
	s.lastQuery = query
	if s.order != nil {
		*s.order = append(*s.order, "handler-list")
	}
	return []*fakeEntity{}, nil
}

// ListPage _：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - opts：可选参数/配置项
//
// 返回：
// - result1：返回结果（类型：*query.PagedResult[*fakeEntity]）
// - err：错误信息（nil 表示成功）
func (s *stubAppService) ListPage(ctx context.Context, opts *query.PaginationOptions) (*query.PagedResult[*fakeEntity], error) {
	s.lastCtx = ctx
	s.lastPage = opts
	if s.order != nil {
		*s.order = append(*s.order, "handler")
	}
	return &query.PagedResult[*fakeEntity]{Data: []*fakeEntity{}, Total: 0, Page: 1, Size: 10}, nil
}

// CountByQuery context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - params：参数值（具体语义见函数上下文）（类型：*query.QueryRequest）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (s *stubAppService) CountByQuery(context.Context, *query.QueryRequest) (int64, error) {
	return 0, nil
}

// CreateAll 批量创建实体（测试 stub，无操作）。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - items：待创建实体列表
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *stubAppService) CreateAll(ctx context.Context, items []*fakeEntity) error {
	_ = ctx
	_ = items
	s.createAllCalls++
	return nil
}

// UpdateAll context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - items：参数值（具体语义见函数上下文）（类型：[]*fakeEntity）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *stubAppService) UpdateAll(context.Context, []*fakeEntity) error {
	s.updateAllCalls++
	return nil
}

// DeleteAll 删除对象。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - items：参数值（具体语义见函数上下文）（类型：[]int64）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *stubAppService) DeleteAll(context.Context, []int64) error {
	s.deleteAllCalls++
	return nil
}

// Validate 校验输入是否满足约束。
//
// 参数：
// - entity：实体对象
//
// 返回：
// - err：错误信息（nil 表示成功）
func (s *stubAppService) Validate(entity *fakeEntity) error {
	if s.cfg != nil && s.cfg.AutoValidate && s.validator != nil {
		return s.validator.Validate(entity)
	}
	return nil
}

// Config 返回当前值。
//
// 返回：
// - result：返回的实例（类型：*appcrud.ServiceConfig）
func (s *stubAppService) Config() *appcrud.ServiceConfig { return s.cfg }

// UpdateConfig 更新配置。
//
// 参数：
// - cfg：配置
func (s *stubAppService) UpdateConfig(cfg *appcrud.ServiceConfig) {
	s.cfg = cloneServiceConfig(cfg)
}

// SetValidator 设置当前值。
//
// 参数：
// - v：校验器（用于请求体/业务校验；会注入到 application 与 API 层）（类型：validate.IValidator）
func (s *stubAppService) SetValidator(v validate.IValidator) { s.validator = v }

func formatFakeID(id int64) string {
	if id == 0 {
		return ""
	}
	return fmt.Sprintf("%d", id)
}

// TestRouteBuilderMiddlewareChain 验证 RouteBuilderMiddlewareChain。
func TestRouteBuilderMiddlewareChain(t *testing.T) {
	var order []string
	svc := newStubAppService(&order)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Middleware(func(ctx httpx.IContext, next func() error) error {
		order = append(order, "builder")
		return next()
	})

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.HTTP.Middlewares = append(cfg.HTTP.Middlewares, func(ctx httpx.IContext, next func() error) error {
			order = append(order, "route")
			return next()
		})
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

	expected := []string{"builder", "route", "handler"}
	if !reflect.DeepEqual(order, expected) {
		t.Fatalf("unexpected middleware order: want %v, got %v", expected, order)
	}
}

// TestRouteBuilder_OperatorExtractorInjectsOperatorIntoContext 验证 RouteBuilder OperatorExtractorInjectsOperatorIntoContext。
func TestRouteBuilder_OperatorExtractorInjectsOperatorIntoContext(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Audit.OperatorExtractor = func(c httpx.IContext) (string, bool) {
			return "alice", true
		}
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
	r := httptest.NewRequest("POST", "/items", strings.NewReader(`{"id":1}`))
	ctx, err := nethttp.NewBaseContext(w, r)
	if err != nil {
		t.Fatalf("NewBaseContext returned error: %v", err)
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if svc.lastCtx == nil {
		t.Fatalf("expected service ctx to be captured")
	}
	if op := contextx.Operator(svc.lastCtx); op != "alice" {
		t.Fatalf("expected operator alice in ctx, got %q", op)
	}
}

func TestRouteBuilder_CreateRejectsUnknownJSONFields(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc)
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

	handler := group.Handlers["POST /items"]
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(`{"id":1,"nmae":"typo"}`))
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
	if svc.lastCreated != nil {
		t.Fatalf("expected Create not to be called on unknown field, got %+v", svc.lastCreated)
	}
}

func TestRouteBuilder_CreateBatchRejectsUnknownJSONFields(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Routing.EnableBatch = true
		cfg.Routing.EnableUpdate = false
		cfg.Routing.EnableDelete = false
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler := group.Handlers["POST /items/batch"]
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/items/batch", strings.NewReader(`[{"id":1,"nmae":"typo"}]`))
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
	if svc.createAllCalls != 0 {
		t.Fatalf("expected CreateAll not to be called on unknown field, got %d", svc.createAllCalls)
	}
}

func TestRouteBuilder_DeleteBatchRejectsUnknownJSONFields(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Routing.EnableBatch = true
		cfg.Routing.EnableCreate = false
		cfg.Routing.EnableUpdate = false
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler := group.Handlers["DELETE /items/batch"]
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/items/batch", strings.NewReader(`{"ids":[1],"unexpected":true}`))
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
	if svc.deleteAllCalls != 0 {
		t.Fatalf("expected DeleteAll not to be called on unknown field, got %d", svc.deleteAllCalls)
	}
}

func TestRouteBuilder_DeleteBatchRejectsEmptyIDs(t *testing.T) {
	svc := newStubAppService(nil)
	builder, err := NewApiBuilder[*fakeEntity, int64](svc)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	builder.Route(func(cfg *RouteConfig[int64]) {
		cfg.Routing.BasePath = "/items"
		cfg.Routing.EnableBatch = true
		cfg.Routing.EnableCreate = false
		cfg.Routing.EnableUpdate = false
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	handler := group.Handlers["DELETE /items/batch"]
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/items/batch", strings.NewReader(`{"ids":[]}`))
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
	if svc.deleteAllCalls != 0 {
		t.Fatalf("expected DeleteAll not to be called on empty ids, got %d", svc.deleteAllCalls)
	}
}

type hookAwareStub struct {
	*stubAppService
	hooks *appcrud.Hooks[*fakeEntity, int64]
}

// SetHooks 设置当前值。
//
// 参数：
// - h：处理函数/回调（类型：*appcrud.Hooks[*fakeEntity, int64]）
func (s *hookAwareStub) SetHooks(h *appcrud.Hooks[*fakeEntity, int64]) { s.hooks = h }

// TestRestfulBuilderAppliesHooks 验证 RestfulBuilderAppliesHooks。
func TestRestfulBuilderAppliesHooks(t *testing.T) {
	svc := &hookAwareStub{stubAppService: newStubAppService(nil)}
	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}

	var fluent IApiBuilder[*fakeEntity, int64] = builder
	fluent.Hooks(func(h *appcrud.Hooks[*fakeEntity, int64]) {
		h.BeforeCreate = func(ctx context.Context, entity *fakeEntity) error { return nil }
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}
	if svc.hooks == nil || svc.hooks.BeforeCreate == nil {
		t.Fatalf("expected hooks to be applied to service")
	}
}

// TestServiceConfigOverride 验证 ServiceConfigOverride。
func TestServiceConfigOverride(t *testing.T) {
	var order []string
	svc := newStubAppService(&order)
	// 默认配置
	def := svc.Config()
	if def == nil {
		t.Fatalf("default service config should not be nil")
	}
	// 设置一个明显的值以便断言
	def.AutoValidate = false
	def.MaxBatchSize = 10
	svc.UpdateConfig(def)

	builder, err := NewApiBuilder[*fakeEntity, int64](svc, nil)
	if err != nil {
		t.Fatalf("NewApiBuilder returned error: %v", err)
	}
	// 通过 Service() 覆盖配置
	builder.Service(func(cfg *appcrud.ServiceConfig) {
		cfg.AutoValidate = true
		cfg.MaxBatchSize = 99
	})

	group := testutil.NewMockRouteGroup()
	if err := builder.Build(group); err != nil {
		t.Fatalf("build failed: %v", err)
	}

	// 验证已应用覆盖配置
	cfg := svc.Config()
	if cfg == nil {
		t.Fatalf("service config should not be nil after build")
	}
	if !cfg.AutoValidate || cfg.MaxBatchSize != 99 {
		t.Fatalf("service config override not applied, got: AutoValidate=%v, MaxBatchSize=%d", cfg.AutoValidate, cfg.MaxBatchSize)
	}
}
