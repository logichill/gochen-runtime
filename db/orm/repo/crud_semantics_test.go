package repo_test

import (
	"context"
	"gochen-runtime/db/orm/lite"
	"gochen/domain/meta"
	"gochen/domain/spec"
	"testing"
	"time"

	repopkg "gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/domain/audited"
	"gochen/domain/crud"
	"gochen/errors"

	_ "modernc.org/sqlite"
)

type specItem struct {
	crud.Entity[int64]
	TenantID       string  `json:"tenant_id" dim:"tenant" managed:"true"`
	ManagedScopeID int64   `json:"managed_scope_id" dim:"scope" managed:"true"`
	OwnerID        string  `json:"owner_id" dim:"owner" managed:"true"`
	Name           string  `json:"name"`
	Amount         int64   `json:"amount"`
	Note           *string `json:"note"`
}

type auditedItem struct {
	audited.AuditedEntity[int64]
	Name string `json:"name"`
}

type tenantItem struct {
	crud.Entity[int64]
	TenantID string `json:"tenant_id" dim:"tenant" managed:"true"`
	Name     string `json:"name"`
}

func (t *tenantItem) GetTenantID() string   { return t.TenantID }
func (t *tenantItem) SetTenantID(id string) { t.TenantID = id }

type counterGen struct{ n int64 }

func (g *counterGen) Next() (int64, error) {
	g.n++
	return g.n, nil
}

func newEngine(t *testing.T) orm.IOrm {
	t.Helper()
	database, err := stdsql.NewWithContext(context.Background(), db.DBConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	engine, err := lite.New(database)
	if err != nil {
		t.Fatalf("lite.New: %v", err)
	}
	return engine
}

func execSQL(t *testing.T, engine orm.IOrm, statements ...string) {
	t.Helper()
	for _, statement := range statements {
		if _, err := engine.Database().Exec(context.Background(), statement); err != nil {
			t.Fatalf("exec %q: %v", statement, err)
		}
	}
}

func requireCode(t *testing.T, err error, code errors.ErrorCode, context string) {
	t.Helper()
	if !errors.Is(err, code) || errors.Code(err) != code {
		t.Fatalf("%s: err=%v, want code %v", context, err, code)
	}
}

func requireNoError(t *testing.T, err error, context string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", context, err)
	}
}

func newSpecRepo(t *testing.T) (*repopkg.Repo[*specItem, int64], orm.IOrm) {
	t.Helper()
	engine := newEngine(t)
	execSQL(t, engine, `CREATE TABLE spec_items (
		id INTEGER PRIMARY KEY,
		version INTEGER NOT NULL,
		tenant_id TEXT NOT NULL,
		managed_scope_id INTEGER NOT NULL,
		owner_id TEXT NOT NULL,
		name TEXT NOT NULL,
		amount INTEGER NOT NULL,
		note TEXT NULL)`)
	repository, err := repopkg.NewRepo[*specItem, int64](engine, "spec_items",
		repopkg.WithNamedCondition[*specItem, int64]("amount_at_least", func(args []any) (orm.Condition, error) {
			return orm.Condition{Expr: "amount >= ?", Args: args}, nil
		}))
	requireNoError(t, err, "NewRepo")
	return repository, engine
}

func seedSpecItems(t *testing.T, repository *repopkg.Repo[*specItem, int64]) {
	t.Helper()
	note := "note"
	items := []*specItem{
		{Entity: crud.Entity[int64]{ID: 1}, TenantID: "t1", ManagedScopeID: 10, OwnerID: "u1", Name: "alpha", Amount: 1},
		{Entity: crud.Entity[int64]{ID: 2}, TenantID: "t1", ManagedScopeID: 20, OwnerID: "u2", Name: "beta", Amount: 2, Note: &note},
		{Entity: crud.Entity[int64]{ID: 3}, TenantID: "t2", ManagedScopeID: 10, OwnerID: "u1", Name: "gamma", Amount: 3},
	}
	for _, item := range items {
		requireNoError(t, repository.Create(context.Background(), item, spec.All()), "seed create")
	}
}

func TestSpecReadTranslation(t *testing.T) {
	repository, _ := newSpecRepo(t)
	seedSpecItems(t, repository)
	ctx := context.Background()

	list := func(s spec.Spec) []*specItem {
		t.Helper()
		items, err := repository.List(ctx, 0, 100, s)
		requireNoError(t, err, "List("+s.Canonical()+")")
		return items
	}

	if got := list(spec.Eq(meta.ByField("Name"), "beta")); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("Eq(Name)=%+v", got)
	}
	if got := list(spec.Eq(meta.ByField("TenantID"), "t1")); len(got) != 2 {
		t.Fatalf("Eq(@tenant)=%d rows, want 2", len(got))
	}
	if got := list(spec.In(meta.ByField("Amount"), 1, 3)); len(got) != 2 {
		t.Fatalf("In(Amount)=%d rows, want 2", len(got))
	}
	if got := list(spec.IsNull(meta.ByField("Note"))); len(got) != 2 {
		t.Fatalf("IsNull(Note)=%d rows, want 2", len(got))
	}
	combined := spec.And(
		spec.Eq(meta.ByField("TenantID"), "t1"),
		spec.Or(spec.Eq(meta.ByField("Name"), "alpha"), spec.Eq(meta.ByField("Name"), "gamma")),
	)
	if got := list(combined); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("And/Or=%+v", got)
	}
	if got := list(spec.Named("amount_at_least", 2)); len(got) != 2 {
		t.Fatalf("Named=%d rows, want 2", len(got))
	}
	if got := list(spec.Named("amount_at_least", 3)); len(got) != 1 {
		t.Fatalf("Named(3)=%d rows, want 1", len(got))
	}

	_, err := repository.List(ctx, 0, 100, spec.Eq(meta.ByField("Missing"), 1))
	requireCode(t, err, errors.Internal, "unknown spec field")

	_, err = repository.Get(ctx, 1, spec.Eq(meta.ByDimension("owner"), "u2"))
	requireCode(t, err, errors.NotFound, "Get with non-matching spec")
}

func TestWriteSpecWithCheck(t *testing.T) {
	repository, _ := newSpecRepo(t)
	ctx := context.Background()

	item := &specItem{Entity: crud.Entity[int64]{ID: 1}, TenantID: "t1", ManagedScopeID: 10, OwnerID: "u1", Name: "alpha"}
	requireNoError(t, repository.Create(ctx, item, spec.Eq(meta.ByDimension("owner"), "u1")), "create within scope")

	mismatch := &specItem{Entity: crud.Entity[int64]{ID: 2}, TenantID: "t2", ManagedScopeID: 10, OwnerID: "u1", Name: "beta"}
	requireCode(t, repository.Create(ctx, mismatch, spec.Eq(meta.ByDimension("owner"), "u2")), errors.Forbidden, "create outside scope")

	requireCode(t, repository.Create(ctx, &specItem{Entity: crud.Entity[int64]{ID: 3}, TenantID: "t1", ManagedScopeID: 10, OwnerID: "u1"}, spec.Eq(meta.ID(), 3)), errors.Internal, "create spec references id role")
	requireCode(t, repository.Create(ctx, &specItem{Entity: crud.Entity[int64]{ID: 4}, TenantID: "t1", ManagedScopeID: 10, OwnerID: "u1"}, spec.Named("amount_at_least", 1)), errors.Internal, "create spec with named condition")
	requireCode(t, repository.Update(ctx, item, spec.Named("amount_at_least", 1)), errors.Internal, "update spec with named condition")

	item.Name = "changed"
	requireCode(t, repository.Update(ctx, item, spec.Eq(meta.ByDimension("owner"), "u2")), errors.Forbidden, "update outside scope")
}

func TestUpdateMissClassificationAndImmutability(t *testing.T) {
	repository, engine := newSpecRepo(t)
	seedSpecItems(t, repository)
	ctx := context.Background()

	missing := &specItem{Entity: crud.Entity[int64]{ID: 99}}
	requireCode(t, repository.Update(ctx, missing, spec.All()), errors.NotFound, "update missing row")

	current, err := repository.Get(ctx, 1, spec.All())
	requireNoError(t, err, "Get")
	if current.Version != 0 {
		t.Fatalf("fresh version=%d, want 0", current.Version)
	}

	stale := &specItem{Entity: crud.Entity[int64]{ID: 1, Version: 5}, TenantID: "t1", ManagedScopeID: 10, OwnerID: "u1", Name: "stale"}
	requireCode(t, repository.Update(ctx, stale, spec.All()), errors.Concurrency, "update stale version")
	if stale.Version != 5 {
		t.Fatalf("failed update mutated entity version to %d, want 5", stale.Version)
	}

	current.Name = "renamed"
	current.TenantID = "hijacked"
	current.ManagedScopeID = 999
	current.OwnerID = "hijacked"
	requireCode(t, repository.Update(ctx, current, spec.All()), errors.Forbidden, "protected input")
	current.TenantID = "t1"
	current.ManagedScopeID = 10
	current.OwnerID = "u1"
	requireNoError(t, repository.Update(ctx, current, spec.All()), "payload update")
	if current.Version != 1 {
		t.Fatalf("entity version=%d, want 1", current.Version)
	}

	var tenant, owner, name string
	var scope, version int64
	row := engine.Database().QueryRow(ctx,
		`SELECT tenant_id, managed_scope_id, owner_id, name, version FROM spec_items WHERE id = ?`, 1)
	requireNoError(t, row.Scan(&tenant, &scope, &owner, &name, &version), "scan updated row")
	if tenant != "t1" || scope != 10 || owner != "u1" {
		t.Fatalf("access-control columns were overwritten: tenant=%q scope=%d owner=%q", tenant, scope, owner)
	}
	if name != "renamed" || version != 1 {
		t.Fatalf("payload update not persisted: name=%q version=%d", name, version)
	}

	requireCode(t, repository.Delete(ctx, &specItem{Entity: crud.Entity[int64]{ID: 1, Version: 0}}, spec.All()), errors.Concurrency, "delete stale version")
	requireCode(t, repository.Delete(ctx, &specItem{Entity: crud.Entity[int64]{ID: 99}}, spec.All()), errors.NotFound, "delete missing row")
}

func TestSoftDeleteVisibilityAndRestore(t *testing.T) {
	engine := newEngine(t)
	execSQL(t, engine, `CREATE TABLE audited_items (
		id INTEGER PRIMARY KEY,
		version INTEGER NOT NULL,
		created_at TIMESTAMP NOT NULL,
		created_by TEXT NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		updated_by TEXT NOT NULL,
		deleted_at TIMESTAMP NULL,
		deleted_by TEXT NULL,
		name TEXT NOT NULL)`)
	repository, err := repopkg.NewRepo[*auditedItem, int64](engine, "audited_items")
	requireNoError(t, err, "NewRepo")
	ctx := context.Background()

	item := &auditedItem{AuditedEntity: audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 1}}, Name: "first"}
	requireNoError(t, repository.Create(ctx, item, spec.All()), "create")

	loaded, err := repository.Get(ctx, 1, spec.All())
	requireNoError(t, err, "Get")
	requireNoError(t, loaded.SoftDeleteBy("alice", time.Now()), "SoftDeleteBy")
	requireNoError(t, repository.Update(ctx, loaded, spec.All()), "soft delete via update")
	if loaded.Version != 1 || !loaded.IsDeleted() {
		t.Fatalf("after soft delete version=%d deleted=%v", loaded.Version, loaded.IsDeleted())
	}

	_, err = repository.Get(ctx, 1, spec.All())
	requireCode(t, err, errors.NotFound, "Get soft-deleted row")
	if _, err := repository.GetWithDeleted(ctx, 1, spec.All()); err != nil {
		t.Fatalf("GetWithDeleted: %v", err)
	}
	if list, err := repository.List(ctx, 0, 10, spec.All()); err != nil || len(list) != 0 {
		t.Fatalf("List included soft-deleted rows: %d err=%v", len(list), err)
	}
	if list, err := repository.ListDeleted(ctx, 0, 10, spec.All()); err != nil || len(list) != 1 {
		t.Fatalf("ListDeleted=%d err=%v, want 1", len(list), err)
	}
	requireCode(t, repository.Delete(ctx, loaded, spec.All()), errors.NotFound, "Delete soft-deleted row")

	restored, err := repository.GetWithDeleted(ctx, 1, spec.All())
	requireNoError(t, err, "GetWithDeleted")
	requireNoError(t, restored.Restore(), "Restore")
	requireNoError(t, repository.Restore(ctx, restored, spec.All()), "repository Restore")
	back, err := repository.Get(ctx, 1, spec.All())
	requireNoError(t, err, "Get after restore")
	if back.IsDeleted() || back.Version != 2 {
		t.Fatalf("after restore deleted=%v version=%d", back.IsDeleted(), back.Version)
	}

	requireCode(t, repository.Restore(ctx, &auditedItem{AuditedEntity: audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 1, Version: 2}}}, spec.All()), errors.Conflict, "restore active row")
	requireCode(t, repository.Restore(ctx, &auditedItem{AuditedEntity: audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 99}}}, spec.All()), errors.NotFound, "restore missing row")

	second := &auditedItem{AuditedEntity: audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 2}}, Name: "second"}
	requireNoError(t, repository.Create(ctx, second, spec.All()), "create second")
	secondLoaded, err := repository.Get(ctx, 2, spec.All())
	requireNoError(t, err, "Get second")
	requireNoError(t, secondLoaded.SoftDeleteBy("alice", time.Now()), "SoftDeleteBy second")
	requireNoError(t, repository.Update(ctx, secondLoaded, spec.All()), "soft delete second")
	requireCode(t, repository.Restore(ctx, &auditedItem{AuditedEntity: audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 2, Version: 0}}}, spec.All()), errors.Concurrency, "restore stale version")

	third := &auditedItem{AuditedEntity: audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 3}}, Name: "third"}
	requireNoError(t, repository.Create(ctx, third, spec.All()), "create third")
	requireNoError(t, repository.Delete(ctx, third, spec.All()), "physical delete")
	if _, err := repository.GetWithDeleted(ctx, 3, spec.All()); !errors.Is(err, errors.NotFound) {
		t.Fatalf("physical delete left row behind: %v", err)
	}

	fourth := &auditedItem{AuditedEntity: audited.AuditedEntity[int64]{Entity: crud.Entity[int64]{ID: 4}}, Name: "fourth"}
	requireNoError(t, repository.Create(ctx, fourth, spec.All()), "create fourth")
	fourthLoaded, err := repository.Get(ctx, 4, spec.All())
	requireNoError(t, err, "Get fourth")
	requireNoError(t, fourthLoaded.SoftDeleteBy("alice", time.Now()), "SoftDeleteBy fourth")
	requireNoError(t, repository.Update(ctx, fourthLoaded, spec.All()), "soft delete fourth")
	requireNoError(t, repository.Purge(ctx, fourthLoaded, spec.All()), "purge")
	if _, err := repository.GetWithDeleted(ctx, 4, spec.All()); !errors.Is(err, errors.NotFound) {
		t.Fatalf("purge left row behind: %v", err)
	}
}

func TestIsolationStampAndStrictBoundary(t *testing.T) {
	engine := newEngine(t)
	execSQL(t, engine, `CREATE TABLE tenant_items (
		id INTEGER PRIMARY KEY,
		version INTEGER NOT NULL,
		tenant_id TEXT NOT NULL,
		name TEXT NOT NULL)`)
	repository, err := repopkg.NewRepo[*tenantItem, int64](engine, "tenant_items",
		repopkg.WithIsolation[*tenantItem, int64](repopkg.IsolationCols{}))
	requireNoError(t, err, "NewRepo")

	ctxA, err := contextx.WithTenantID(context.Background(), "tenant-a")
	requireNoError(t, err, "WithTenantID")
	ctxB, err := contextx.WithTenantID(context.Background(), "tenant-b")
	requireNoError(t, err, "WithTenantID")

	item := &tenantItem{Entity: crud.Entity[int64]{ID: 1}, TenantID: "forged", Name: "first"}
	requireNoError(t, repository.Create(ctxA, item, spec.All()), "create")
	if item.TenantID != "tenant-a" {
		t.Fatalf("isolation not stamped: %q", item.TenantID)
	}
	var tenant string
	row := engine.Database().QueryRow(ctxA, `SELECT tenant_id FROM tenant_items WHERE id = ?`, 1)
	requireNoError(t, row.Scan(&tenant), "scan tenant")
	if tenant != "tenant-a" {
		t.Fatalf("persisted tenant=%q", tenant)
	}

	_, err = repository.Get(ctxB, 1, spec.All())
	requireCode(t, err, errors.NotFound, "cross-tenant Get")

	_, err = repository.Get(context.Background(), 1, spec.All())
	requireCode(t, err, errors.Forbidden, "Get without isolation key")

	loaded, err := repository.Get(ctxA, 1, spec.All())
	requireNoError(t, err, "Get in tenant")
	loaded.TenantID = "tenant-b"
	loaded.Name = "renamed"
	requireCode(t, repository.Update(ctxA, loaded, spec.All()), errors.Forbidden, "isolation input")
	loaded.TenantID = "tenant-a"
	requireNoError(t, repository.Update(ctxA, loaded, spec.All()), "payload update")
	row = engine.Database().QueryRow(ctxA, `SELECT tenant_id FROM tenant_items WHERE id = ?`, 1)
	requireNoError(t, row.Scan(&tenant), "scan tenant after update")
	if tenant != "tenant-a" {
		t.Fatalf("update moved row across tenants: %q", tenant)
	}

	other := &tenantItem{Entity: crud.Entity[int64]{ID: 2}, Name: "other"}
	requireNoError(t, repository.Create(ctxB, other, spec.All()), "create in tenant-b")
	listA, err := repository.List(ctxA, 0, 10, spec.All())
	if err != nil || len(listA) != 1 || listA[0].ID != 1 {
		t.Fatalf("tenant-a list=%+v err=%v", listA, err)
	}

	err = repository.Update(ctxB, &tenantItem{Entity: crud.Entity[int64]{ID: 1}}, spec.All())
	requireCode(t, err, errors.NotFound, "cross-tenant probe must not leak existence")
}
