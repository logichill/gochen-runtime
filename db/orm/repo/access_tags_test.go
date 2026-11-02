package repo_test

import (
	"context"
	"testing"

	repopkg "gochen-runtime/db/orm/repo"
	"gochen/domain/crud"
	"gochen/domain/spec"
)

type hiddenFieldItem struct {
	crud.Entity[int64]
	Name       string `json:"name"`
	Hidden     string `json:"-" gorm:"type:text"`
	Explicit   string `json:"-" gorm:"column:explicit"`
	JSONHidden string `json:"-"`
	ORMIgnored string `json:"orm_ignored" gorm:"-"`
	DBIgnored  string `json:"db_ignored" db:"-"`
}

func TestRepoUpdatesPersistedJSONHiddenFields(t *testing.T) {
	engine := newEngine(t)
	execSQL(t, engine, `CREATE TABLE hidden_field_items (
		id INTEGER PRIMARY KEY, version INTEGER NOT NULL,
		name TEXT NOT NULL, hidden TEXT NOT NULL, explicit TEXT NOT NULL)`)
	repository, err := repopkg.NewRepo[*hiddenFieldItem, int64](engine, "hidden_field_items")
	requireNoError(t, err, "NewRepo")
	ctx := context.Background()
	item := &hiddenFieldItem{
		Entity: crud.Entity[int64]{ID: 1}, Name: "visible",
		Hidden: "before", Explicit: "before",
		JSONHidden: "excluded", ORMIgnored: "excluded", DBIgnored: "excluded",
	}
	requireNoError(t, repository.Create(ctx, item, spec.All()), "create")
	loaded, err := repository.Get(ctx, item.ID, spec.All())
	requireNoError(t, err, "load")
	if loaded.Hidden != "before" || loaded.Explicit != "before" {
		t.Fatalf("created hidden fields = %q/%q", loaded.Hidden, loaded.Explicit)
	}
	loaded.Hidden, loaded.Explicit = "after", "after"
	loaded.JSONHidden, loaded.ORMIgnored, loaded.DBIgnored = "excluded", "excluded", "excluded"
	requireNoError(t, repository.Update(ctx, loaded, spec.All()), "update")
	stored, err := repository.Get(ctx, item.ID, spec.All())
	requireNoError(t, err, "read update")
	if stored.Hidden != "after" || stored.Explicit != "after" {
		t.Fatalf("updated hidden fields = %q/%q, want after/after", stored.Hidden, stored.Explicit)
	}
	if stored.JSONHidden != "" || stored.ORMIgnored != "" || stored.DBIgnored != "" {
		t.Fatalf("excluded fields were persisted: %+v", stored)
	}
}
