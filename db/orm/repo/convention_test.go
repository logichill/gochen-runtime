package repo_test

import (
	"context"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/domain/crud"
	"gochen/errors"
	"gochen/testkit/assert"
	"gochen/testkit/require"
)

type TestTenantEntity struct {
	crud.TenantEntity[int64]
	OrgID  int64  `json:"org_id"`
	Name   string `json:"name"`
	Amount int64  `json:"amount"`
}

func (e *TestTenantEntity) Validate() error { return nil }

type TestCustomTenantEntity struct {
	crud.Entity[int64]
	TenantCode string `json:"tenant_code"`
	OrgID      int64  `json:"org_id"`
	Name       string `json:"name"`
}

func (e *TestCustomTenantEntity) GetTenantID() string  { return e.TenantCode }
func (e *TestCustomTenantEntity) SetTenantID(t string) { e.TenantCode = t }
func (e *TestCustomTenantEntity) Validate() error      { return nil }

type TestPlainEntity struct {
	crud.Entity[int64]
	Name string `json:"name"`
}

func (e *TestPlainEntity) Validate() error { return nil }

type TestAuditedEntity struct {
	crud.Entity[int64]
	Name      string     `json:"name"`
	DeletedAt *time.Time `json:"deleted_at"`
	DeletedBy string     `json:"deleted_by"`
}

func (e *TestAuditedEntity) Validate() error { return nil }

type TestAuditOnlyEntity struct {
	crud.Entity[int64]
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

var _ audited.IAuditable = (*TestAuditOnlyEntity)(nil)

func (e *TestAuditOnlyEntity) GetCreatedAt() time.Time  { return e.CreatedAt }
func (e *TestAuditOnlyEntity) SetCreatedAt(t time.Time) { e.CreatedAt = t }
func (e *TestAuditOnlyEntity) GetCreatedBy() string     { return e.CreatedBy }
func (e *TestAuditOnlyEntity) GetUpdatedAt() time.Time  { return e.UpdatedAt }
func (e *TestAuditOnlyEntity) SetUpdatedAt(t time.Time) { e.UpdatedAt = t }
func (e *TestAuditOnlyEntity) GetUpdatedBy() string     { return e.UpdatedBy }
func (e *TestAuditOnlyEntity) GetDeletedBy() *string    { return nil }
func (e *TestAuditOnlyEntity) SetCreatedInfo(by string, at time.Time) {
	e.CreatedBy = by
	e.CreatedAt = at
}
func (e *TestAuditOnlyEntity) SetUpdatedInfo(by string, at time.Time) {
	e.UpdatedBy = by
	e.UpdatedAt = at
	e.Version++
}
func (e *TestAuditOnlyEntity) Validate() error { return nil }

func setupTestDB(t *testing.T, naming *db.NamingConvention) orm.IOrm {
	t.Helper()
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: ":memory:",
		Naming:   naming,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	engine, err := lite.New(database)
	require.NoError(t, err)
	return engine
}

func TestWithIsolation_NamingAndConflict(t *testing.T) {
	// 1. Default isolation with built-in naming
	engineDefault := setupTestDB(t, nil)
	r1, err := repo.NewRepo[*TestTenantEntity, int64](engineDefault, "tenant_entities",
		repo.WithIsolation[*TestTenantEntity, int64](repo.IsolationCols{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "tenant_entities", r1.ResourceKind())

	// 2. Default isolation with global custom naming mapping to custom tenant_code
	customNaming := &db.NamingConvention{
		TenantColumn: "tenant_code",
		ScopeColumn:  "org_id",
	}
	engineCustom := setupTestDB(t, customNaming)
	r2, err := repo.NewRepo[*TestCustomTenantEntity, int64](engineCustom, "custom_tenants",
		repo.WithIsolation[*TestCustomTenantEntity, int64](repo.IsolationCols{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "custom_tenants", r2.ResourceKind())

	// 3. Calling WithIsolation with semantically equivalent column succeeds without conflict
	rEquiv, err := repo.NewRepo[*TestTenantEntity, int64](engineDefault, "tenant_entities",
		repo.WithIsolation[*TestTenantEntity, int64](repo.IsolationCols{}),
		repo.WithIsolation[*TestTenantEntity, int64](repo.IsolationCols{
			Column: "tenant_id",
		}),
	)
	require.NoError(t, err)
	assert.Equal(t, "tenant_entities", rEquiv.ResourceKind())

	// 4. Conflict detection: calling WithIsolation with conflicting custom columns
	_, err = repo.NewRepo[*TestTenantEntity, int64](engineDefault, "tenant_entities",
		repo.WithIsolation[*TestTenantEntity, int64](repo.IsolationCols{}),
		repo.WithIsolation[*TestTenantEntity, int64](repo.IsolationCols{
			Column: "org_id",
		}),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.InvalidInput))

	// 5. Conflict detection: calling WithIsolation with conflicting resolve
	_, err = repo.NewRepo[*TestTenantEntity, int64](engineDefault, "tenant_entities",
		repo.WithIsolation[*TestTenantEntity, int64](repo.IsolationCols{}),
		repo.WithIsolation[*TestTenantEntity, int64](repo.IsolationCols{
			Column: "tenant_id",
			Resolve: func(ctx context.Context) (string, error) {
				return "org-1", nil
			},
		}),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.InvalidInput))

	// 6. Conflict detection: calling WithIsolation with two different custom resolves
	resolveA := func(ctx context.Context) (string, error) { return "a", nil }
	resolveB := func(ctx context.Context) (string, error) { return "b", nil }
	_, err = repo.NewRepo[*TestPlainEntity, int64](engineDefault, "plain_items",
		repo.WithIsolation[*TestPlainEntity, int64](repo.IsolationCols{
			Column:  "name",
			Resolve: resolveA,
		}),
		repo.WithIsolation[*TestPlainEntity, int64](repo.IsolationCols{
			Column:  "name",
			Resolve: resolveB,
		}),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.InvalidInput))
}

func TestWithScope_NamingAndConventions(t *testing.T) {
	customNaming := &db.NamingConvention{
		TenantColumn:  "tenant_id",
		ScopeColumn:   "org_id",
		VersionColumn: "version",
	}
	engineCustom := setupTestDB(t, customNaming)

	scopedRepo, err := repo.NewRepo[*TestTenantEntity, int64](
		engineCustom,
		"orders",
		repo.WithResourceKind[*TestTenantEntity, int64]("order"),
		repo.WithRequireScope[*TestTenantEntity, int64](),
		repo.WithScope[*TestTenantEntity, int64](repo.ScopeCols{}),
	)
	require.NoError(t, err)
	assert.Equal(t, "order", scopedRepo.ResourceKind())
	assert.True(t, scopedRepo.HasScopeDeclaration())

	// Partial WithScope specification does not drop version column convention
	scopedPartial, err := repo.NewRepo[*TestTenantEntity, int64](
		engineCustom,
		"orders",
		repo.WithResourceKind[*TestTenantEntity, int64]("order"),
		repo.WithRequireScope[*TestTenantEntity, int64](),
		repo.WithScope[*TestTenantEntity, int64](repo.ScopeCols{
			ManagedScopeID: "org_id",
		}),
	)
	require.NoError(t, err)
	assert.True(t, scopedPartial.HasScopeDeclaration())
}

func TestWithAudited_Conventions(t *testing.T) {
	engineDefault := setupTestDB(t, nil)

	// Audited entity with deleted_at and deleted_by succeeds
	auditedRepo, err := repo.NewRepo[*TestAuditedEntity, int64](
		engineDefault,
		"audited_items",
		repo.WithAudited[*TestAuditedEntity, int64](),
	)
	require.NoError(t, err)
	assert.Equal(t, "audited_items", auditedRepo.ResourceKind())

	// Plain entity missing deleted_at/deleted_by fails fast in WithAudited
	_, err = repo.NewRepo[*TestPlainEntity, int64](
		engineDefault,
		"plain_items",
		repo.WithAudited[*TestPlainEntity, int64](),
	)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.InvalidInput))
}

func TestAuditableHardDelete(t *testing.T) {
	engineDefault := setupTestDB(t, nil)

	// DDL without deleted_at/deleted_by columns
	ctx := context.Background()
	_, err := engineDefault.Database().Exec(ctx, `CREATE TABLE audit_only (
		id INTEGER PRIMARY KEY,
		name TEXT,
		created_at TIMESTAMP,
		created_by TEXT,
		updated_at TIMESTAMP,
		updated_by TEXT,
		version INTEGER
	)`)
	require.NoError(t, err)

	// Entity implementing IAuditable but not ISoftDeletable should succeed without softDelete
	r, err := repo.NewRepo[*TestAuditOnlyEntity, int64](engineDefault, "audit_only")
	require.NoError(t, err)

	// Create record
	item := &TestAuditOnlyEntity{
		Entity: crud.Entity[int64]{ID: 101, Version: 1},
		Name:   "hard-delete-target",
	}
	ctxWithActor, err := contextx.WithOperator(ctx, "operator-alice")
	require.NoError(t, err)
	err = r.Create(ctxWithActor, item)
	require.NoError(t, err)
	assert.Equal(t, "operator-alice", item.CreatedBy)

	// Delete should perform physical hard delete
	err = r.Delete(ctx, 101)
	require.NoError(t, err)

	// Subsequent Get should return NotFound
	_, err = r.Get(ctx, 101)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.NotFound))
}

func TestNewRepo_DoesNotImplicitlyActivateScope(t *testing.T) {
	customNaming := &db.NamingConvention{
		TenantColumn: "tenant_id",
		ScopeColumn:  "org_id",
	}
	engineCustom := setupTestDB(t, customNaming)

	// 普通 NewRepo 即使实体含有 org_id，也不能隐式开启 Scope
	plainRepo, err := repo.NewRepo[*TestTenantEntity, int64](engineCustom, "tenant_entities")
	require.NoError(t, err)
	assert.False(t, plainRepo.HasScopeDeclaration())
}
