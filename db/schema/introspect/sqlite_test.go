package introspect

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gochen-runtime/db/schema"
	"gochen-runtime/db/sql/stdsql"
	"gochen/db"

	_ "modernc.org/sqlite"
)

func TestSQLiteIntrospectorInspect(t *testing.T) {
	t.Parallel()

	database := openSQLiteDatabase(t, filepath.Join(t.TempDir(), "schema.db"))
	execDDL(t, database, `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL DEFAULT '',
		email TEXT,
		code TEXT UNIQUE
	)`)
	execDDL(t, database, `CREATE UNIQUE INDEX idx_users_email ON users(email)`)
	execDDL(t, database, `CREATE INDEX idx_users_name ON users(name)`)
	execDDL(t, database, `CREATE INDEX idx_users_email_partial ON users(email) WHERE email IS NOT NULL`)
	execDDL(t, database, `CREATE INDEX idx_users_lower_name ON users(lower(name))`)
	execDDL(t, database, `CREATE INDEX idx_users_lower_email_name ON users(lower(email), name)`)

	got, err := NewSQLite().Inspect(context.Background(), database)
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected schema, got nil")
	}

	users, ok := got.FindTable("", "users")
	if !ok {
		t.Fatalf("expected users table, got %#v", got.Tables)
	}
	if len(users.Columns) != 4 {
		t.Fatalf("users columns = %d, want 4", len(users.Columns))
	}

	id, ok := users.FindColumn("id")
	if !ok {
		t.Fatal("expected id column")
	}
	if !id.PrimaryKey || !id.AutoIncrement || id.Nullable {
		t.Fatalf("unexpected id column: %#v", id)
	}

	name, ok := users.FindColumn("name")
	if !ok {
		t.Fatal("expected name column")
	}
	if name.Nullable || name.DefaultValue != "''" {
		t.Fatalf("unexpected name column: %#v", name)
	}

	emailIndex, ok := users.FindIndex("idx_users_email")
	if !ok {
		t.Fatal("expected idx_users_email index")
	}
	if !emailIndex.Unique || len(emailIndex.Columns) != 1 || emailIndex.Columns[0] != "email" {
		t.Fatalf("unexpected idx_users_email: %#v", emailIndex)
	}
	if !hasUniqueIndexOn(users.Indexes, "code") {
		t.Fatalf("expected inline unique constraint index for code, got %#v", users.Indexes)
	}
	if idx, ok := users.FindIndex("idx_users_email_partial"); !ok || !idx.Unsupported {
		t.Fatalf("expected partial index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if idx, ok := users.FindIndex("idx_users_lower_name"); !ok || !idx.Unsupported {
		t.Fatalf("expected expression index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if idx, ok := users.FindIndex("idx_users_lower_email_name"); !ok || !idx.Unsupported {
		t.Fatalf("expected mixed expression index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if !hasWarning(got.Warnings, "partial index users.idx_users_email_partial") {
		t.Fatalf("expected partial index warning, got %#v", got.Warnings)
	}
	if !hasWarning(got.Warnings, "expression index users.idx_users_lower_name") {
		t.Fatalf("expected expression index warning, got %#v", got.Warnings)
	}
	if !hasWarning(got.Warnings, "expression index users.idx_users_lower_email_name") {
		t.Fatalf("expected mixed expression index warning, got %#v", got.Warnings)
	}
}

func TestSQLiteIntrospectorDetectsAutoIncrementAcrossValidColumnLayouts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ddl  string
	}{
		{
			name: "not-null-before-primary-key",
			ddl:  `CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT, name TEXT)`,
		},
		{
			name: "quoted-identifier",
			ddl:  `CREATE TABLE users ("id" INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)`,
		},
		{
			name: "backtick-identifier",
			ddl:  "CREATE TABLE users (`id` INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)",
		},
		{
			name: "bracket-identifier",
			ddl:  `CREATE TABLE users ([id] INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			database := openSQLiteDatabase(t, filepath.Join(t.TempDir(), "schema.db"))
			execDDL(t, database, tt.ddl)

			got, err := NewSQLite().Inspect(context.Background(), database)
			if err != nil {
				t.Fatalf("Inspect failed: %v", err)
			}
			users, ok := got.FindTable("", "users")
			if !ok {
				t.Fatalf("expected users table, got %#v", got.Tables)
			}
			id, ok := users.FindColumn("id")
			if !ok {
				t.Fatal("expected id column")
			}
			if !id.AutoIncrement {
				t.Fatalf("expected autoincrement id column for ddl %q, got %#v", tt.ddl, id)
			}
		})
	}
}

func TestSQLiteIntrospectorInspectWithSingleConnection(t *testing.T) {
	database, err := stdsql.NewWithContext(context.Background(), db.DBConfig{
		Driver:       "sqlite",
		Database:     filepath.Join(t.TempDir(), "single-conn.db"),
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	execDDL(t, database, `CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT UNIQUE)`)
	execDDL(t, database, `CREATE INDEX idx_users_email ON users(email)`)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := NewSQLite().Inspect(ctx, database)
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if _, ok := got.FindTable("", "users"); !ok {
		t.Fatalf("expected users table, got %#v", got.Tables)
	}
}

func TestSQLiteIntrospectorInspectEscapedQuotedIdentifiers(t *testing.T) {
	t.Parallel()

	database := openSQLiteDatabase(t, filepath.Join(t.TempDir(), "schema.db"))
	execDDL(t, database, `CREATE TABLE "weird""table" ("id" INTEGER PRIMARY KEY, "quo""ted" TEXT)`)
	execDDL(t, database, `CREATE INDEX "idx""weird" ON "weird""table"("quo""ted")`)

	got, err := NewSQLite().Inspect(context.Background(), database)
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	table, ok := got.FindTable("", `weird"table`)
	if !ok {
		t.Fatalf("expected quoted table, got %#v", got.Tables)
	}
	if _, ok := table.FindColumn(`quo"ted`); !ok {
		t.Fatalf("expected quoted column, got %#v", table.Columns)
	}
	index, ok := table.FindIndex(`idx"weird`)
	if !ok {
		t.Fatalf("expected quoted index, got %#v", table.Indexes)
	}
	if len(index.Columns) != 1 || index.Columns[0] != `quo"ted` {
		t.Fatalf("unexpected quoted index columns: %#v", index.Columns)
	}
}

func hasUniqueIndexOn(indexes []schema.Index, column string) bool {
	for _, index := range indexes {
		if index.Unique && len(index.Columns) == 1 && index.Columns[0] == column {
			return true
		}
	}
	return false
}

func openSQLiteDatabase(t *testing.T, path string) db.IDatabase {
	t.Helper()
	database, err := stdsql.NewWithContext(context.Background(), db.DBConfig{
		Driver:   "sqlite",
		Database: path,
	})
	if err != nil {
		t.Fatalf("open sqlite db failed: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})
	return database
}

func execDDL(t *testing.T, database db.IDatabase, ddl string) {
	t.Helper()
	if _, err := database.Exec(context.Background(), ddl); err != nil {
		t.Fatalf("Exec DDL failed: %v", err)
	}
}

func hasWarning(warnings []string, sub string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, sub) {
			return true
		}
	}
	return false
}
