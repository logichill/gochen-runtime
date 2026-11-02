package quick_test

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	"gochen-runtime/host/config"
	"gochen-runtime/quick"
	"gochen/app/crud"
	"gochen/auth"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	gochenhttp "gochen/httpx"
	"gochen/testkit/assert"
	"gochen/testkit/require"
)

type TestQuickEntity struct {
	auth.TenantEntity[int64]
	OrgID  int64  `json:"org_id" db:"org_id"`
	Title  string `json:"title" db:"title"`
	Amount int64  `json:"amount" db:"amount"`
}

func (e *TestQuickEntity) Validate() error { return nil }

func getFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}

func setupTestDB(t *testing.T) orm.IOrm {
	t.Helper()
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	require.NoError(t, err)

	_, err = database.Exec(context.Background(), `CREATE TABLE test_quick_entities (
		id INTEGER PRIMARY KEY,
		tenant_id TEXT NOT NULL,
		org_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		amount INTEGER NOT NULL,
		version INTEGER NOT NULL DEFAULT 1
	)`)
	require.NoError(t, err)

	engine, err := lite.New(database, lite.WithNamingConvention(db.NamingConvention{
		TenantColumn: "tenant_id",
		ScopeColumn:  "org_id",
	}))
	require.NoError(t, err)
	return engine
}

func newQuickApp(t *testing.T, r *repo.Repo[*TestQuickEntity, int64]) *crud.Application[*TestQuickEntity, int64] {
	t.Helper()
	app, err := quick.App[*TestQuickEntity, int64](r, crud.Options[*TestQuickEntity, int64]{
		Resource:   "quick_item",
		Authorizer: auth.Allow(),
	})
	require.NoError(t, err)
	return app
}

func TestQuick_FullPipeline(t *testing.T) {
	engine := setupTestDB(t)

	// 1. Repo
	r, err := quick.IsolatedRepo[*TestQuickEntity, int64](engine, "test_quick_entities",
		repo.WithResourceKind[*TestQuickEntity, int64]("quick_item"))
	require.NoError(t, err)
	assert.Equal(t, "quick_item", r.ResourceKind())

	// 2. App directly from Repo
	app := newQuickApp(t, r)

	// 3. Verify App functionality
	ctx, err := contextx.WithTenantID(context.Background(), "tenant-test")
	require.NoError(t, err)

	item := &TestQuickEntity{
		OrgID:  100,
		Title:  "Item 1",
		Amount: 50,
	}
	item.SetID(1)
	item.SetTenantID("tenant-test")

	require.NoError(t, app.Create(ctx, item))

	fetched, err := app.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, "Item 1", fetched.Title)

	list, err := app.List(ctx, 0, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, len(list))

	// 4. REST Registrar
	reg := quick.RESTRegistrar[*TestQuickEntity, int64](app)
	require.NotNil(t, reg)

	// 5. Module
	modCtor := quick.Module("quick_module", reg)
	require.NotNil(t, modCtor)

	// 6. Server with dynamic free port
	port := getFreePort(t)
	srv, err := quick.New(config.WithName("test-service"), config.WithPort(port)).Modules(modCtor).Build()
	require.NoError(t, err)
	require.NotNil(t, srv)

	runCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = srv.Start(runCtx)
	if err != nil && !stderrors.Is(err, context.Canceled) && !stderrors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected server start error: %v", err)
	}
}

func TestQuick_EndToEnd_HTTPRoundTrip(t *testing.T) {
	engine := setupTestDB(t)

	// 1. Repo & App 装配
	r, err := quick.IsolatedRepo[*TestQuickEntity, int64](engine, "test_quick_entities")
	require.NoError(t, err)
	app := newQuickApp(t, r)

	// 2. 模拟认证中间件注入租户
	tenantMw := func(c gochenhttp.IContext, next func() error) error {
		pCtx, err := contextx.WithTenantID(c.RequestContext(), "tenant-roundtrip")
		if err != nil {
			return err
		}
		c.SetContext(c.RequestContext().WithContext(pCtx))
		return next()
	}

	// 3. 模块声明与路由挂载
	itemModule := quick.Module("items",
		quick.RESTRegistrar[*TestQuickEntity, int64](app),
		quick.WithMiddlewares(tenantMw))

	port := getFreePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- quick.New(config.WithName("roundtrip-service"), config.WithPort(port)).Modules(itemModule).Run(ctx)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d/api/v1/items", port)
	client := &http.Client{Timeout: 3 * time.Second}

	// 4. 等待 HTTP 服务就绪
	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			return true
		}
		return false
	}, 3*time.Second, 50*time.Millisecond)

	// 5. 发送真实 POST 请求创建实体
	postPayload := `{"id": 201, "org_id": 200, "title": "RoundTrip Item", "amount": 1280}`
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewBufferString(postPayload))
	require.NoError(t, err)
	postReq.Header.Set("Content-Type", "application/json")

	postResp, err := client.Do(postReq)
	require.NoError(t, err)
	defer postResp.Body.Close()
	assert.Equal(t, http.StatusOK, postResp.StatusCode)

	// 6. 发送真实 GET 请求查询单条详情
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/201", nil)
	require.NoError(t, err)

	getResp, err := client.Do(getReq)
	require.NoError(t, err)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusOK, getResp.StatusCode)

	var getResult struct {
		Code string           `json:"code"`
		Data *TestQuickEntity `json:"data"`
	}
	err = json.NewDecoder(getResp.Body).Decode(&getResult)
	require.NoError(t, err)
	require.NotNil(t, getResult.Data)
	assert.Equal(t, int64(201), getResult.Data.GetID())
	assert.Equal(t, int64(200), getResult.Data.OrgID)
	assert.Equal(t, "RoundTrip Item", getResult.Data.Title)
	assert.Equal(t, int64(1280), getResult.Data.Amount)

	// 7. 发送真实 GET List 请求查询列表
	listReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL, nil)
	require.NoError(t, err)

	listResp, err := client.Do(listReq)
	require.NoError(t, err)
	defer listResp.Body.Close()
	assert.Equal(t, http.StatusOK, listResp.StatusCode)

	var listResult struct {
		Code string `json:"code"`
		Data struct {
			Data  []*TestQuickEntity `json:"data"`
			Total int64              `json:"total"`
		} `json:"data"`
	}
	err = json.NewDecoder(listResp.Body).Decode(&listResult)
	require.NoError(t, err)
	assert.Equal(t, int64(1), listResult.Data.Total)
	assert.Equal(t, 1, len(listResult.Data.Data))
	assert.Equal(t, "RoundTrip Item", listResult.Data.Data[0].Title)

	// 8. 验证优雅停机
	cancel()
	err = <-errCh
	if err != nil && !stderrors.Is(err, context.Canceled) {
		t.Fatalf("unexpected stop error: %v", err)
	}
}

func TestQuick_InvalidInputs(t *testing.T) {
	engine := setupTestDB(t)

	r, err := quick.IsolatedRepo[*TestQuickEntity, int64](engine, "test_quick_entities")
	require.NoError(t, err)

	// App nil repository
	_, err = quick.App[*TestQuickEntity, int64](nil, crud.Options[*TestQuickEntity, int64]{
		Resource:   "quick_item",
		Authorizer: auth.Allow(),
	})
	require.Error(t, err)

	// App typed-nil repository
	var typedNilRepo *repo.Repo[*TestQuickEntity, int64]
	_, err = quick.App[*TestQuickEntity, int64](typedNilRepo, crud.Options[*TestQuickEntity, int64]{
		Resource:   "quick_item",
		Authorizer: auth.Allow(),
	})
	require.Error(t, err)

	// App without explicit authorizer
	_, err = quick.App[*TestQuickEntity, int64](r, crud.Options[*TestQuickEntity, int64]{
		Resource: "quick_item",
	})
	require.Error(t, err)

	// App without resource name
	_, err = quick.App[*TestQuickEntity, int64](r, crud.Options[*TestQuickEntity, int64]{
		Authorizer: auth.Allow(),
	})
	require.Error(t, err)

	// Repo with conflicting isolation columns
	_, err = quick.Repo[*TestQuickEntity, int64](engine, "test_quick_entities",
		repo.WithIsolation[*TestQuickEntity, int64](repo.IsolationCols{Column: "tenant_id"}),
		repo.WithIsolation[*TestQuickEntity, int64](repo.IsolationCols{Column: "org_id"}))
	require.Error(t, err)

	// Port above the valid range must fail before startup.
	_, err = quick.New(config.WithPort(70000)).Build()
	require.Error(t, err)
}
