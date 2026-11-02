package repo_test

import (
	"context"
	"testing"
	"time"

	repopkg "gochen-runtime/db/orm/repo"
	"gochen/contextx"
	"gochen/domain/spec"
	"gochen/errors"
	"gochen/testkit/contracttest"
)

func TestProtectedStringsUseExactEquality(t *testing.T) {
	for _, collation := range []string{"NOCASE", "RTRIM"} {
		t.Run(collation, func(t *testing.T) {
			engine := newEngine(t)
			execSQL(t, engine, `CREATE TABLE documents (id INTEGER PRIMARY KEY, version INTEGER NOT NULL,
				created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, created_by TEXT, updated_by TEXT, deleted_by TEXT,
				tenant TEXT NOT NULL, department INTEGER NOT NULL, region TEXT COLLATE `+collation+`, classified BOOLEAN NOT NULL, name TEXT NOT NULL)`)
			r, err := repopkg.NewRepo[*contracttest.Document, int64](engine, "documents",
				repopkg.WithResourceContract[*contracttest.Document, int64](contracttest.Contract()),
				repopkg.WithIsolation[*contracttest.Document, int64](repopkg.IsolationCols{Column: "tenant"}))
			requireNoError(t, err, "repo")
			ctx, err := contextx.WithTenantID(context.Background(), "t1")
			requireNoError(t, err, "tenant")
			region := "north"
			entity := &contracttest.Document{Department: 1, Region: &region, Name: "original"}
			entity.ID = 1
			requireNoError(t, r.Create(ctx, entity, spec.All()), "create")
			candidate, err := r.Get(ctx, entity.ID, spec.All())
			requireNoError(t, err, "get")
			changed := "NORTH"
			if collation == "RTRIM" {
				changed = "north "
			}
			candidate.Region, candidate.Name = &changed, "changed"
			requireCode(t, r.Update(ctx, candidate, spec.All()), errors.Forbidden, "protected string")
			stored, err := r.Get(ctx, entity.ID, spec.All())
			requireNoError(t, err, "reload")
			if stored.Name != "original" || stored.Version != entity.Version || *stored.Region != region || candidate.Version != entity.Version {
				t.Fatalf("rejected write changed state: stored=%+v candidate=%+v", stored, candidate)
			}
			stored.Name = "allowed"
			requireNoError(t, r.Update(ctx, stored, spec.All()), "unchanged protected value")
			requireNoError(t, stored.SoftDeleteBy("alice", time.Now()), "soft-delete candidate")
			requireNoError(t, r.Update(ctx, stored, spec.All()), "soft-delete")
			deleted, err := r.GetWithDeleted(ctx, entity.ID, spec.All())
			requireNoError(t, err, "deleted record")
			expectedVersion := deleted.Version
			requireNoError(t, deleted.Restore(), "restore candidate")
			deleted.Region = &changed
			requireCode(t, r.Restore(ctx, deleted, spec.All()), errors.Forbidden, "restore protected string")
			remaining, err := r.GetWithDeleted(ctx, entity.ID, spec.All())
			requireNoError(t, err, "rejected restore")
			if !remaining.IsDeleted() || remaining.Version != expectedVersion || deleted.Version != expectedVersion || *remaining.Region != region {
				t.Fatalf("rejected restore changed state: %+v", remaining)
			}
			deleted.Region = &region
			requireNoError(t, r.Restore(ctx, deleted, spec.All()), "unchanged restore value")
		})
	}
}
