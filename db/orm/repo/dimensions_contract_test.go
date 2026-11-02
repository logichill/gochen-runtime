package repo_test

import (
	repopkg "gochen-runtime/db/orm/repo"
	"gochen/db/orm"
	"gochen/testkit/contracttest"
	"testing"
)

func TestSQLDimensionsContract(t *testing.T) {
	contracttest.RunDimensions(t, func(t *testing.T) contracttest.Repository {
		r, _ := newDimensionSQL(t)
		return r
	})
}

func newDimensionSQL(t *testing.T) (*repopkg.Repo[*contracttest.Document, int64], orm.IOrm) {
	t.Helper()
	engine := newEngine(t)
	execSQL(t, engine, `CREATE TABLE documents (
			id INTEGER PRIMARY KEY, version INTEGER NOT NULL, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME,
			created_by TEXT, updated_by TEXT, deleted_by TEXT,
			tenant TEXT NOT NULL, department INTEGER NOT NULL, region TEXT, classified BOOLEAN NOT NULL, name TEXT NOT NULL)`)
	r, err := repopkg.NewRepo[*contracttest.Document, int64](engine, "documents",
		repopkg.WithIDGenerator[*contracttest.Document, int64](&counterGen{}),
		repopkg.WithResourceContract[*contracttest.Document, int64](contracttest.Contract()),
		repopkg.WithIsolation[*contracttest.Document, int64](repopkg.IsolationCols{Column: "tenant"}),
		repopkg.WithResourceKind[*contracttest.Document, int64]("document"))
	if err != nil {
		t.Fatal(err)
	}
	return r, engine
}
