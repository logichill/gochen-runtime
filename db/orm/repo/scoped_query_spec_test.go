package repo_test

import (
	"context"
	"testing"
	"time"

	repopkg "gochen-runtime/db/orm/repo"
	"gochen/contextx"
	"gochen/db/orm"
	"gochen/domain/crud"

	"gochen/domain/meta"
	"gochen/domain/spec"
	"gochen/errors"
)

func TestCustomQueryAppliesSpecBeforePaginationAndCount(t *testing.T) {
	repository, _ := newSpecRepo(t)
	seedSpecItems(t, repository)
	condition := spec.And(spec.Eq(meta.ByField("TenantID"), "t1"), spec.Eq(meta.ByField("ManagedScopeID"), int64(20)))
	quer, err := repository.ScopedQueryWithSpec(context.Background(), condition)
	requireNoError(t, err, "conditional query")
	var items []*specItem
	requireNoError(t, quer.Order("id", false).Limit(1).Find(&items), "custom page")
	if len(items) != 1 || items[0].ID != 2 {
		t.Fatalf("page leaks out-of-scope first row: %+v", items)
	}
	quer, err = repository.ScopedQueryWithSpec(context.Background(), condition)
	requireNoError(t, err, "count query")
	count, err := quer.Count()
	requireNoError(t, err, "custom count")
	if count != 1 {
		t.Fatalf("custom count=%d", count)
	}
	_, err = repository.ScopedQueryWithSpec(context.Background(), spec.Eq(meta.ByField("Missing"), 1))
	requireCode(t, err, errors.Internal, "invalid custom query reference")
}

type joinedScopeItem struct {
	crud.Entity[int64]
	TenantID  string `dim:"tenant"`
	Name      string
	DeletedAt *time.Time
}

func TestCustomQueryQualifiesIdentityIsolationAndSoftDeleteAcrossJoin(t *testing.T) {
	engine := newEngine(t)
	execSQL(t, engine,
		`CREATE TABLE joined_items (id INTEGER PRIMARY KEY, version INTEGER NOT NULL, tenant_id TEXT NOT NULL, name TEXT NOT NULL, deleted_at DATETIME NULL)`,
		`CREATE TABLE joined_links (id INTEGER PRIMARY KEY, item_id INTEGER NOT NULL, tenant_id TEXT NOT NULL, deleted_at DATETIME NULL)`,
		`INSERT INTO joined_items VALUES (1,0,'tenant-a','visible',NULL),(2,0,'tenant-b','other',NULL),(3,0,'tenant-a','deleted','2026-01-01')`,
		`INSERT INTO joined_links VALUES (11,1,'unrelated','2026-01-01'),(12,2,'unrelated',NULL),(13,3,'unrelated',NULL)`)
	repository, err := repopkg.NewRepo[*joinedScopeItem, int64](engine, "joined_items", repopkg.WithIsolation[*joinedScopeItem, int64](repopkg.IsolationCols{Column: "tenant_id"}), repopkg.WithSoftDeleteColumns[*joinedScopeItem, int64]("deleted_at", ""))
	requireNoError(t, err, "repository")
	ctx, err := contextx.WithTenantID(t.Context(), "tenant-a")
	requireNoError(t, err, "tenant")
	query, err := repository.ScopedQueryWithSpec(ctx, spec.In(meta.ID(), int64(1), int64(2), int64(3)))
	requireNoError(t, err, "scoped query")
	query.Join(orm.InnerJoin("joined_links", "", orm.On("joined_items.id", "joined_links.item_id")))
	count, err := query.Count()
	requireNoError(t, err, "joined count")
	if count != 1 {
		t.Fatalf("joined authorization/isolation/soft delete count=%d", count)
	}
	var items []*joinedScopeItem
	requireNoError(t, query.SelectExprUnsafe(repopkg.AllowUnsafeSelectExpr(), "joined_items.*").Find(&items), "joined targets")
	if len(items) != 1 || items[0].ID != 1 {
		t.Fatalf("joined targets=%+v", items)
	}
	item, err := repository.Get(ctx, 1, spec.All())
	requireNoError(t, err, "ordinary read after qualified query")
	if item.ID != 1 {
		t.Fatalf("ordinary read=%+v", item)
	}
}
