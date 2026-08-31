package schema_test

import (
	"context"
	"path/filepath"
	"testing"

	"gochen-runtime/db/schema"
	"gochen-runtime/db/schema/diff"
	"gochen-runtime/db/schema/introspect"
	"gochen-runtime/db/schema/render"
	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	"gochen/db/dialect"

	_ "modernc.org/sqlite"
)

func TestDiffRenderAndInspectSQLiteFlow(t *testing.T) {
	t.Parallel()

	database := openSchemaFlowDB(t)
	execSchemaFlowSQL(t, database, `CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT)`)

	current, err := introspect.Inspect(context.Background(), database)
	if err != nil {
		t.Fatalf("Inspect current failed: %v", err)
	}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Name: "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
				{Name: "email", Type: "TEXT", Nullable: false, DefaultValue: "''"},
			},
			Indexes: []schema.Index{{Name: "idx_users_email", Columns: []string{"email"}, Unique: true}},
		},
	}}

	changes := diff.Between(current, desired)
	sqls, err := render.RenderSQL(changes, dialect.New("sqlite"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	for _, stmt := range sqls {
		execSchemaFlowSQL(t, database, stmt)
	}

	after, err := introspect.Inspect(context.Background(), database)
	if err != nil {
		t.Fatalf("Inspect after failed: %v", err)
	}
	users, ok := after.FindTable("", "users")
	if !ok {
		t.Fatal("expected users table")
	}
	if _, ok := users.FindColumn("email"); !ok {
		t.Fatal("expected email column")
	}
	if idx, ok := users.FindIndex("idx_users_email"); !ok || !idx.Unique {
		t.Fatalf("expected unique email index, got %#v ok=%v", idx, ok)
	}
}

func TestDiffRenderSQLiteAttachedSchemaIndexFlow(t *testing.T) {
	t.Parallel()

	database := openSchemaFlowDB(t)
	attachedPath := filepath.Join(t.TempDir(), "tenant.db")
	execSchemaFlowSQL(t, database, `ATTACH DATABASE '`+attachedPath+`' AS tenant`)
	execSchemaFlowSQL(t, database, `CREATE TABLE tenant.users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT)`)

	current := &schema.Schema{Tables: []schema.Table{
		{
			Schema: "tenant",
			Name:   "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
				{Name: "email", Type: "TEXT", Nullable: true},
			},
		},
	}}
	desired := &schema.Schema{Tables: []schema.Table{
		{
			Schema: "tenant",
			Name:   "users",
			Columns: []schema.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
				{Name: "email", Type: "TEXT", Nullable: true},
			},
			Indexes: []schema.Index{{Name: "idx_users_email", Columns: []string{"email"}, Unique: true}},
		},
	}}

	changes := diff.Between(current, desired)
	sqls, err := render.RenderSQL(changes, dialect.New("sqlite"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	want := []string{`CREATE UNIQUE INDEX "tenant"."idx_users_email" ON "users" ("email")`}
	if len(sqls) != len(want) || sqls[0] != want[0] {
		t.Fatalf("RenderSQL() = %#v, want %#v", sqls, want)
	}
	for _, stmt := range sqls {
		execSchemaFlowSQL(t, database, stmt)
	}

	var indexName string
	if err := database.QueryRow(
		context.Background(),
		`SELECT name FROM tenant.sqlite_master WHERE type = 'index' AND name = ?`,
		"idx_users_email",
	).Scan(&indexName); err != nil {
		t.Fatalf("query attached schema index failed: %v", err)
	}
	if indexName != "idx_users_email" {
		t.Fatalf("index name = %q, want idx_users_email", indexName)
	}
}

func TestRenderDownSQLiteAttachedSchemaDropsTargetIndex(t *testing.T) {
	t.Parallel()

	database := openSchemaFlowDB(t)
	attachedPath := filepath.Join(t.TempDir(), "tenant.db")
	execSchemaFlowSQL(t, database, `ATTACH DATABASE '`+attachedPath+`' AS tenant`)
	execSchemaFlowSQL(t, database, `CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT)`)
	execSchemaFlowSQL(t, database, `CREATE TABLE tenant.users (id INTEGER PRIMARY KEY AUTOINCREMENT, email TEXT)`)
	execSchemaFlowSQL(t, database, `CREATE INDEX idx_users_email ON users (email)`)
	execSchemaFlowSQL(t, database, `CREATE INDEX tenant.idx_users_email ON users (email)`)

	changes := diff.Changes{
		{
			Kind:  diff.KindAddIndex,
			Table: "tenant.users",
			Index: &schema.Index{Name: "idx_users_email", Columns: []string{"email"}},
		},
	}
	sqls, err := render.RenderDownSQL(changes, dialect.New("sqlite"))
	if err != nil {
		t.Fatalf("RenderDownSQL failed: %v", err)
	}
	want := []string{`DROP INDEX "tenant"."idx_users_email"`}
	if len(sqls) != len(want) || sqls[0] != want[0] {
		t.Fatalf("RenderDownSQL() = %#v, want %#v", sqls, want)
	}
	for _, stmt := range sqls {
		execSchemaFlowSQL(t, database, stmt)
	}

	if got := schemaFlowIndexCount(t, database, "main", "idx_users_email"); got != 1 {
		t.Fatalf("main index count = %d, want 1", got)
	}
	if got := schemaFlowIndexCount(t, database, "tenant", "idx_users_email"); got != 0 {
		t.Fatalf("tenant index count = %d, want 0", got)
	}
}

func openSchemaFlowDB(t *testing.T) db.IDatabase {
	t.Helper()
	database, err := stdsql.NewWithContext(context.Background(), db.DBConfig{
		Driver:   "sqlite",
		Database: filepath.Join(t.TempDir(), "schema-flow.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

func execSchemaFlowSQL(t *testing.T, database db.IDatabase, stmt string) {
	t.Helper()
	if _, err := database.Exec(context.Background(), stmt); err != nil {
		t.Fatalf("Exec %q failed: %v", stmt, err)
	}
}

func schemaFlowIndexCount(t *testing.T, database db.IDatabase, schemaName string, indexName string) int {
	t.Helper()
	query := ""
	switch schemaName {
	case "main":
		query = `SELECT count(*) FROM main.sqlite_master WHERE type = 'index' AND name = ?`
	case "tenant":
		query = `SELECT count(*) FROM tenant.sqlite_master WHERE type = 'index' AND name = ?`
	default:
		t.Fatalf("unsupported schema %q", schemaName)
	}
	var count int
	if err := database.QueryRow(
		context.Background(),
		query,
		indexName,
	).Scan(&count); err != nil {
		t.Fatalf("query %s index count failed: %v", schemaName, err)
	}
	return count
}
