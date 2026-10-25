package repo

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen/auth/scoped"
	"gochen/contextx"
	"gochen/db"
	"gochen/errors"
	"gochen/testkit/require"
	_ "modernc.org/sqlite"
)

// SQL Capture 回归基线。
//
// 目的：把当前 Repo 生成的**真实 SQL 与参数**锁成 golden，
// 作为"隐式列推断 → 构造期显式声明"改造（阶段 2/3）的安全网。
// 任何 WHERE 条件的增删都会让本文件的断言失败，从而暴露安全语义漂移。
//
// 注意：本文件断言的是**当前**行为，不代表当前行为都是理想的；
// 改造时应有意识地更新 golden，并在 review 中确认每一处变化都是预期的。

// capturedStatement 记录一次落到数据库的语句。
type capturedStatement struct {
	Kind string // query | queryRow | exec
	SQL  string
	Args []any
}

// capturingDB 包装 db.IDatabase，记录所有经过的语句后原样委托。
type capturingDB struct {
	inner db.IDatabase

	mu         sync.Mutex
	statements []capturedStatement
}

func newCapturingDB(inner db.IDatabase) *capturingDB {
	return &capturingDB{inner: inner}
}

func (c *capturingDB) record(kind, query string, args []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = append(c.statements, capturedStatement{Kind: kind, SQL: query, Args: args})
}

// reset 清空已记录语句，便于逐操作断言。
func (c *capturingDB) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statements = nil
}

// captured 返回已记录语句快照。
func (c *capturingDB) captured() []capturedStatement {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]capturedStatement(nil), c.statements...)
}

// lastSQL 返回最后一条语句的规范化 SQL。
func (c *capturingDB) lastSQL(tb testing.TB) string {
	tb.Helper()
	stmts := c.captured()
	require.True(tb, len(stmts) > 0, "expected at least one captured statement")
	return normalizeSQL(stmts[len(stmts)-1].SQL)
}

// lastArgs 返回最后一条语句的参数。
func (c *capturingDB) lastArgs(tb testing.TB) []any {
	tb.Helper()
	stmts := c.captured()
	require.True(tb, len(stmts) > 0, "expected at least one captured statement")
	return stmts[len(stmts)-1].Args
}

// DatasourceID 转发底层身份：包装层不得吞掉数据源标识，
// 否则跨数据源事务传播校验因“无法验证”被拒绝。
func (c *capturingDB) DatasourceID() string {
	if provider, ok := c.inner.(db.IDatasourceIdentityProvider); ok && provider != nil {
		return provider.DatasourceID()
	}
	return ""
}

func (c *capturingDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	c.record("query", query, args)
	return c.inner.Query(ctx, query, args...)
}

func (c *capturingDB) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	c.record("queryRow", query, args)
	return c.inner.QueryRow(ctx, query, args...)
}

func (c *capturingDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	c.record("exec", query, args)
	return c.inner.Exec(ctx, query, args...)
}

func (c *capturingDB) Begin(ctx context.Context) (db.ITransaction, error) {
	tx, err := c.inner.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &capturingTx{ITransaction: tx, owner: c}, nil
}

func (c *capturingDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	tx, err := c.inner.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &capturingTx{ITransaction: tx, owner: c}, nil
}

// capturingTx 让事务内的语句同样落入捕获。
//
// 批量写全部发生在事务里；不包装事务的话，恰恰是最需要观测的批量路径完全不可见。
type capturingTx struct {
	db.ITransaction
	owner *capturingDB
}

func (t *capturingTx) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	t.owner.record("query", query, args)
	return t.ITransaction.Query(ctx, query, args...)
}

func (t *capturingTx) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	t.owner.record("queryRow", query, args)
	return t.ITransaction.QueryRow(ctx, query, args...)
}

func (t *capturingTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	t.owner.record("exec", query, args)
	return t.ITransaction.Exec(ctx, query, args...)
}

// DialectName / MaxBindParameters 必须显式转发。
//
// 内嵌的是 db.ITransaction **接口**：接口内嵌只提升该接口方法集里的方法，
// 底层动态类型上的可选能力方法一概不会被提升。批量写全在事务里，
// 这里漏一个就等于事务内的分片预算悄悄退回默认值。
func (t *capturingTx) DialectName() string {
	if provider, ok := t.ITransaction.(db.IDialectNameProvider); ok {
		return provider.DialectName()
	}
	return ""
}

func (t *capturingTx) MaxBindParameters() int {
	if provider, ok := t.ITransaction.(db.IBindParameterLimitProvider); ok {
		return provider.MaxBindParameters()
	}
	return dialect.DefaultMaxBindParameters
}

// DatasourceID 同样必须显式转发：吞掉身份会让事务会话的归属校验
// 因“无法验证”而被拒绝。
func (t *capturingTx) DatasourceID() string {
	if provider, ok := t.ITransaction.(db.IDatasourceIdentityProvider); ok {
		return provider.DatasourceID()
	}
	return ""
}

func (c *capturingDB) Ping(ctx context.Context) error { return c.inner.Ping(ctx) }
func (c *capturingDB) Close() error                   { return c.inner.Close() }

// DialectName 透传底层方言，保证 ORM 生成与真实驱动一致的 SQL。
func (c *capturingDB) DialectName() string {
	if provider, ok := c.inner.(db.IDialectNameProvider); ok {
		return provider.DialectName()
	}
	return ""
}

// MaxBindParameters 透传底层预算。
//
// 与 DialectName 同理：可选能力接口不转发就会被静默丢掉，表现为"配了预算却
// 没生效"。任何包装 db.IDatabase 的装饰器都要注意这一点，测试装饰器也不例外。
func (c *capturingDB) MaxBindParameters() int {
	if provider, ok := c.inner.(db.IBindParameterLimitProvider); ok {
		return provider.MaxBindParameters()
	}
	return dialect.DefaultMaxBindParameters
}

// normalizeSQL 压缩空白，让 golden 断言不受换行/缩进影响。
func normalizeSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

// captureEntity 同时具备 tenant / scope / owner / version 列，用于覆盖全部安全过滤路径。
type captureEntity struct {
	ID             int64  `json:"id"`
	Version        uint64 `json:"version"`
	TenantID       string `json:"tenant_id"`
	ManagedScopeID int64  `json:"managed_scope_id"`
	OwnerID        string `json:"owner_id"`
	Name           string `json:"name"`
}

func (e *captureEntity) GetID() int64       { return e.ID }
func (e *captureEntity) GetVersion() uint64 { return e.Version }

// 声明了隔离列的实体必须实现 ITenantEntity（构造期断言，§1.5.1）。
func (e *captureEntity) GetTenantID() string         { return e.TenantID }
func (e *captureEntity) SetTenantID(tenantID string) { e.TenantID = tenantID }

// newCaptureRepo 搭建 sqlite + 语句捕获 + 显式安全列的 Repo。
func newCaptureRepo(tb testing.TB) (*Repo[*captureEntity, int64], *capturingDB) {
	tb.Helper()

	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(), `
		CREATE TABLE capture_entities (
			id INTEGER PRIMARY KEY,
			version INTEGER NOT NULL DEFAULT 0,
			tenant_id TEXT NOT NULL DEFAULT '',
			managed_scope_id INTEGER NOT NULL DEFAULT 0,
			owner_id TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL DEFAULT ''
		)`)
	require.NoError(tb, err)

	capturing := newCapturingDB(database)
	ormEngine, err := lite.New(capturing)
	require.NoError(tb, err)

	repository, err := NewRepo[*captureEntity, int64](ormEngine, "capture_entities",
		WithScope[*captureEntity, int64](ScopeCols{ManagedScopeID: "managed_scope_id", OwnerID: "owner_id", Revision: "version"}),
		WithIsolation[*captureEntity, int64](IsolationCols{Column: "tenant_id"}),
	)
	require.NoError(tb, err)
	return repository, capturing
}

// scopedContext 绑定租户与数据范围。
func scopedContext(tb testing.TB, tenantID string, scopeIDs ...int64) context.Context {
	tb.Helper()
	ctx := context.Background()
	if tenantID != "" {
		var err error
		ctx, err = contextx.WithTenantID(ctx, tenantID)
		require.NoError(tb, err)
	}
	if len(scopeIDs) > 0 {
		var err error
		ctx, err = scoped.WithDataScope(ctx, scoped.Filtered(scopeIDs...))
		require.NoError(tb, err)
	}
	return ctx
}

// constrainedContext 在 scopedContext 之上追加受控通道写约束。
//
// 声明了范围列的仓储在写路径必须拿到约束，否则 fail-closed
// （见 constraint_channel.go）。golden 写用例因此都要经此入口。
func constrainedContext(tb testing.TB, ctx context.Context, entityType string, resource scoped.ResourceConstraint) context.Context {
	tb.Helper()
	provider := scoped.SingleEntityConstraint(entityType, scoped.WriteConstraint{
		Resources: []scoped.ResourceConstraint{resource},
	})
	return scoped.WithConstraint(ctx, provider)
}

// --- Golden：读路径 ---

// 声明了隔离列的 Repo，在 ctx 缺租户时必须拒绝——绝不能"不加过滤"
// 从而把所有租户的数据读出来（§4.1 Fail-Closed）。
func TestSQLCapture_IsolatedRepo_FailsClosedWithoutTenant(t *testing.T) {
	repository, capturing := newCaptureRepo(t)
	ctx, err := scoped.WithDataScope(context.Background(), scoped.Global())
	require.NoError(t, err)

	capturing.reset()
	_, err = repository.Get(ctx, 7)

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.Forbidden))
	require.Equal(t, 0, len(capturing.captured()), "拒绝的请求不得触达数据库")
}

func TestSQLCapture_Get_WithTenantAndScope(t *testing.T) {
	repository, capturing := newCaptureRepo(t)
	ctx := scopedContext(t, "tenant-a", 101, 202)

	capturing.reset()
	_, _ = repository.Get(ctx, 7)

	// 关键安全断言：租户与范围条件必须原子地进入同一条 WHERE。
	require.Equal(t,
		`SELECT * FROM "capture_entities" WHERE tenant_id = ? AND managed_scope_id IN (?, ?) AND id = ? LIMIT ?`,
		capturing.lastSQL(t))
	require.Equal(t, []any{"tenant-a", int64(101), int64(202), int64(7), 1}, capturing.lastArgs(t))
}

func TestSQLCapture_Count_WithTenantAndScope(t *testing.T) {
	repository, capturing := newCaptureRepo(t)
	ctx := scopedContext(t, "tenant-a", 101)

	capturing.reset()
	_, err := repository.Count(ctx)
	require.NoError(t, err)

	require.Equal(t,
		`SELECT COUNT(*) FROM "capture_entities" WHERE tenant_id = ? AND managed_scope_id = ?`,
		capturing.lastSQL(t))
	require.Equal(t, []any{"tenant-a", int64(101)}, capturing.lastArgs(t))
}

func TestSQLCapture_List_WithTenantAndScope(t *testing.T) {
	repository, capturing := newCaptureRepo(t)
	ctx := scopedContext(t, "tenant-a", 101, 202)

	capturing.reset()
	_, err := repository.List(ctx, 10, 20)
	require.NoError(t, err)

	require.Equal(t,
		`SELECT * FROM "capture_entities" WHERE tenant_id = ? AND managed_scope_id IN (?, ?) LIMIT ? OFFSET ?`,
		capturing.lastSQL(t))
	require.Equal(t, []any{"tenant-a", int64(101), int64(202), 20, 10}, capturing.lastArgs(t))
}

// --- Golden：写路径 ---

func TestSQLCapture_Create_StampsTenantAndScope(t *testing.T) {
	repository, capturing := newCaptureRepo(t)
	ctx := constrainedContext(t, scopedContext(t, "tenant-a", 101), "capture_entities",
		scoped.ResourceConstraint{Kind: "capture_entities", ResourceID: "1", TenantID: "tenant-a", ManagedScopeID: 101})

	capturing.reset()
	entity := &captureEntity{ID: 1, Name: "n1"}
	require.NoError(t, repository.Create(ctx, entity))

	// Create 必须把归属写进实体（否则后续读路径永远查不到）。
	require.Equal(t, "tenant-a", entity.TenantID)
	require.Equal(t, int64(101), entity.ManagedScopeID)

	stmts := capturing.captured()
	require.True(t, len(stmts) > 0)
	require.True(t, strings.Contains(normalizeSQL(stmts[len(stmts)-1].SQL), `INSERT INTO "capture_entities"`))
}

func TestSQLCapture_Update_AtomicGuard(t *testing.T) {
	repository, capturing := newCaptureRepo(t)
	ctx := constrainedContext(t, scopedContext(t, "tenant-a", 101), "capture_entities",
		scoped.ResourceConstraint{Kind: "capture_entities", ResourceID: "1", TenantID: "tenant-a", ManagedScopeID: 101, Revision: "0"})

	seed := &captureEntity{ID: 1, Name: "n1"}
	require.NoError(t, repository.Create(ctx, seed))

	capturing.reset()
	seed.Name = "n2"
	_ = repository.Update(ctx, seed)

	last := capturing.lastSQL(t)
	// 原子守卫：租户 + 范围 与主键在同一条 UPDATE 的 WHERE 中，不做"先查后判断"。
	require.True(t, strings.HasPrefix(last, `UPDATE "capture_entities" SET`), last)
	require.True(t, strings.HasSuffix(last,
		"WHERE tenant_id = ? AND managed_scope_id = ? AND id = ? AND tenant_id = ? AND managed_scope_id = ? AND version = ?"), last)

	// 写路径收敛后的事实：声明了范围列的仓储走受控约束路径，
	// version 乐观锁条件恒在 WHERE 中（原基线"普通 Update 不含 version"的
	// 缺口已随写路径统一而消除）。
	// 两组 tenant/scope 条件分别来自 ctx 数据范围与写约束——约束只增不减，
	// 二者求交后语义更严，不会互相抵消。
	require.True(t, strings.Contains(last, "version = ?"), last)
}

func TestSQLCapture_Delete_AtomicGuard(t *testing.T) {
	repository, capturing := newCaptureRepo(t)
	ctx := constrainedContext(t, scopedContext(t, "tenant-a", 101), "capture_entities",
		scoped.ResourceConstraint{Kind: "capture_entities", ResourceID: "1", TenantID: "tenant-a", ManagedScopeID: 101, Revision: "0"})

	seed := &captureEntity{ID: 1, Name: "n1"}
	require.NoError(t, repository.Create(ctx, seed))

	capturing.reset()
	_ = repository.Delete(ctx, seed.ID)

	require.Equal(t,
		`DELETE FROM "capture_entities" WHERE tenant_id = ? AND managed_scope_id = ? AND id = ? `+
			`AND tenant_id = ? AND managed_scope_id = ? AND version = ?`,
		capturing.lastSQL(t))
}

// --- Golden：无安全列声明的实体不得被追加安全过滤 ---

type plainCaptureEntity struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (e *plainCaptureEntity) GetID() int64       { return e.ID }
func (e *plainCaptureEntity) GetVersion() uint64 { return 0 }

func TestSQLCapture_PlainEntity_NoSecurityFilter(t *testing.T) {
	database, err := stdsql.New(db.DBConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	_, err = database.Exec(context.Background(),
		"CREATE TABLE plain_entities (id INTEGER PRIMARY KEY, name TEXT NOT NULL DEFAULT '')")
	require.NoError(t, err)

	capturing := newCapturingDB(database)
	ormEngine, err := lite.New(capturing)
	require.NoError(t, err)

	repository, err := NewRepo[*plainCaptureEntity, int64](ormEngine, "plain_entities")
	require.NoError(t, err)

	// 即便 ctx 上绑定了租户与范围，没有安全列的实体也不得被追加过滤。
	ctx := scopedContext(t, "tenant-a", 101)
	capturing.reset()
	_, _ = repository.Get(ctx, 7)

	require.Equal(t,
		`SELECT * FROM "plain_entities" WHERE id = ? LIMIT ?`,
		capturing.lastSQL(t))
}
