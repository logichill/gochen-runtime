package render

import (
	"reflect"
	"strings"
	"testing"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/schema"
	"gochen-runtime/db/schema/diff"
)

func TestRenderSQLSQLite(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:  diff.KindAddTable,
			Table: "users",
			Target: &schema.Table{
				Name: "users",
				Columns: []schema.Column{
					{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
					{Name: "name", Type: "TEXT", Nullable: false, DefaultValue: "''"},
					{Name: "email", Type: "TEXT", Nullable: true},
				},
				Indexes: []schema.Index{
					{Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
				},
			},
		},
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "age", Type: "INTEGER", Nullable: false, DefaultValue: "0"},
		},
		{
			Kind:  diff.KindAddIndex,
			Table: "users",
			Index: &schema.Index{Name: "idx_users_age", Columns: []string{"age"}},
		},
	}

	got, err := RenderSQL(changes, dialect.New("sqlite"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	want := []string{
		"CREATE TABLE \"users\" (\n  \"id\" INTEGER PRIMARY KEY AUTOINCREMENT,\n  \"name\" TEXT NOT NULL DEFAULT '',\n  \"email\" TEXT\n)",
		"CREATE UNIQUE INDEX \"idx_users_email\" ON \"users\" (\"email\")",
		"ALTER TABLE \"users\" ADD COLUMN \"age\" INTEGER NOT NULL DEFAULT 0",
		"CREATE INDEX \"idx_users_age\" ON \"users\" (\"age\")",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RenderSQL() = %#v, want %#v", got, want)
	}
}

func TestRenderSQLAddTableIndexesUseTargetName(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind: diff.KindAddTable,
			Target: &schema.Table{
				Name:    "users",
				Columns: []schema.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
				Indexes: []schema.Index{
					{Name: "idx_users_id", Columns: []string{"id"}},
				},
			},
		},
	}

	got, err := RenderSQL(changes, dialect.New("sqlite"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	want := []string{
		"CREATE TABLE \"users\" (\n  \"id\" INTEGER NOT NULL,\n  PRIMARY KEY (\"id\")\n)",
		"CREATE INDEX \"idx_users_id\" ON \"users\" (\"id\")",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RenderSQL() = %#v, want %#v", got, want)
	}
}

func TestRenderSQLAddTableRejectsMismatchedTableName(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:  diff.KindAddTable,
			Table: "profiles",
			Target: &schema.Table{
				Name:    "users",
				Columns: []schema.Column{{Name: "id", Type: "INTEGER"}},
			},
		},
	}

	_, err := RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "table mismatch") {
		t.Fatalf("RenderSQL error = %v, want table mismatch", err)
	}
}

func TestRenderSQLQuotesByDialect(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "display_name", Type: "VARCHAR(255)", Nullable: false, DefaultValue: "''"},
		},
	}

	mysqlSQL, err := RenderSQL(changes, dialect.New("mysql"))
	if err != nil {
		t.Fatalf("RenderSQL mysql failed: %v", err)
	}
	if mysqlSQL[0] != "ALTER TABLE `users` ADD COLUMN `display_name` VARCHAR(255) NOT NULL DEFAULT ''" {
		t.Fatalf("mysql SQL = %q", mysqlSQL[0])
	}

	postgresSQL, err := RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL postgres failed: %v", err)
	}
	if postgresSQL[0] != `ALTER TABLE "users" ADD COLUMN "display_name" VARCHAR(255) NOT NULL DEFAULT ''` {
		t.Fatalf("postgres SQL = %q", postgresSQL[0])
	}
}

func TestRenderSQLPostgresPreservesAutoIncrementWidth(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind: diff.KindAddTable,
			Target: &schema.Table{
				Name: "users",
				Columns: []schema.Column{
					{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
					{Name: "tenant_id", Type: "BIGINT", PrimaryKey: false, Nullable: false},
				},
			},
		},
	}

	got, err := RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL postgres failed: %v", err)
	}
	want := `CREATE TABLE "users" (
  "id" SERIAL NOT NULL,
  "tenant_id" BIGINT NOT NULL,
  PRIMARY KEY ("id")
)`
	if got[0] != want {
		t.Fatalf("postgres SQL = %q, want %q", got[0], want)
	}

	changes[0].Target.Columns[0] = schema.Column{Name: "id", Type: "BIGINT", PrimaryKey: true, AutoIncrement: true}
	got, err = RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL postgres failed: %v", err)
	}
	if !strings.Contains(got[0], `"id" BIGSERIAL NOT NULL`) || !strings.Contains(got[0], `PRIMARY KEY ("id")`) {
		t.Fatalf("expected BIGSERIAL plus table-level primary key, got %q", got[0])
	}

	changes[0].Target.Columns[0] = schema.Column{Name: "id", Type: "INTEGER4", PrimaryKey: true, AutoIncrement: true}
	got, err = RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL postgres failed: %v", err)
	}
	if !strings.Contains(got[0], `"id" SERIAL NOT NULL`) || !strings.Contains(got[0], `PRIMARY KEY ("id")`) {
		t.Fatalf("expected INTEGER4 auto increment to render SERIAL with table-level primary key, got %q", got[0])
	}
}

func TestRenderSQLPostgresCompositePrimaryKeyUsesSingleTableConstraint(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind: diff.KindAddTable,
			Target: &schema.Table{
				Name: "orders",
				Columns: []schema.Column{
					{Name: "id", Type: "BIGINT", PrimaryKey: true, AutoIncrement: true},
					{Name: "tenant_id", Type: "BIGINT", PrimaryKey: true, Nullable: false},
				},
			},
		},
	}

	got, err := RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL postgres failed: %v", err)
	}
	want := `CREATE TABLE "orders" (
  "id" BIGSERIAL NOT NULL,
  "tenant_id" BIGINT NOT NULL,
  PRIMARY KEY ("id", "tenant_id")
)`
	if got[0] != want {
		t.Fatalf("postgres SQL = %q, want %q", got[0], want)
	}
}

func TestRenderSQLRejectsMySQLNonPrimaryAutoIncrement(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "seq", Type: "BIGINT", AutoIncrement: true},
		},
	}

	if _, err := RenderSQL(changes, dialect.New("mysql")); err == nil || !strings.Contains(err.Error(), "must be primary key") {
		t.Fatalf("RenderSQL mysql error = %v, want primary-key auto increment error", err)
	}

	postgresSQL, err := RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL postgres failed: %v", err)
	}
	if postgresSQL[0] != `ALTER TABLE "users" ADD COLUMN "seq" BIGSERIAL NOT NULL` {
		t.Fatalf("postgres SQL = %q", postgresSQL[0])
	}
}

func TestRenderSQLRejectsSQLiteNonPrimaryAutoIncrement(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "seq", Type: "INTEGER", AutoIncrement: true},
		},
	}

	_, err := RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "must be primary key") {
		t.Fatalf("RenderSQL error = %v, want primary-key auto increment error", err)
	}
}

func TestRenderSQLRejectsSQLiteCompositePrimaryKeyWithAutoIncrement(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind: diff.KindAddTable,
			Target: &schema.Table{
				Name: "orders",
				Columns: []schema.Column{
					{Name: "id", Type: "INTEGER", PrimaryKey: true, AutoIncrement: true},
					{Name: "tenant_id", Type: "INTEGER", PrimaryKey: true},
				},
			},
		},
	}

	_, err := RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "single-column primary key") {
		t.Fatalf("RenderSQL error = %v, want sqlite composite auto-increment rejection", err)
	}
}

func TestRenderAddColumnRejectsSQLiteNotNullWithoutDefault(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "email", Type: "TEXT", Nullable: false},
		},
	}

	_, err := RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "cannot be NOT NULL without default") {
		t.Fatalf("RenderSQL error = %v, want sqlite not-null default rejection", err)
	}

	got, err := RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL postgres failed: %v", err)
	}
	if got[0] != `ALTER TABLE "users" ADD COLUMN "email" TEXT NOT NULL` {
		t.Fatalf("postgres SQL = %#v", got)
	}
}

func TestRenderSQLRejectsUnsafeColumnTypeAndDefault(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "email", Type: "TEXT; DROP TABLE users", Nullable: true},
		},
	}
	_, err := RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema column type fragment") {
		t.Fatalf("RenderSQL error = %v, want unsafe type fragment", err)
	}

	changes[0].Column.Type = "TEXT"
	changes[0].Column.DefaultValue = "'x' -- comment"
	_, err = RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema column default fragment") {
		t.Fatalf("RenderSQL error = %v, want unsafe default fragment", err)
	}

	changes[0].Column.DefaultValue = "(SELECT 1)"
	_, err = RenderSQL(changes, dialect.New("postgres"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema column default fragment") {
		t.Fatalf("RenderSQL error = %v, want dangerous default token", err)
	}

	changes[0].Column.DefaultValue = "VALUES(1)"
	_, err = RenderSQL(changes, dialect.New("postgres"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema column default fragment") {
		t.Fatalf("RenderSQL error = %v, want dangerous values token", err)
	}

	changes[0].Column.DefaultValue = "SET('a')"
	_, err = RenderSQL(changes, dialect.New("mysql"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema column default fragment") {
		t.Fatalf("RenderSQL error = %v, want dangerous set token in default", err)
	}

	changes[0].Column.DefaultValue = "WITH x AS (SELECT 1)"
	_, err = RenderSQL(changes, dialect.New("postgres"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema column default fragment") {
		t.Fatalf("RenderSQL error = %v, want dangerous with token", err)
	}

	for _, keyword := range []string{"COPY users TO STDOUT", "LOAD DATA INFILE 'x'", "DO $$ BEGIN END $$", "REPLACE INTO users VALUES(1)", "MERGE INTO users USING src"} {
		changes[0].Column.DefaultValue = keyword
		_, err = RenderSQL(changes, dialect.New("postgres"))
		if err == nil || !strings.Contains(err.Error(), "unsafe schema column default fragment") {
			t.Fatalf("RenderSQL error = %v, want dangerous token rejection for %q", err, keyword)
		}
	}
}

func TestRenderSQLAllowsCommonColumnTypeAndDefaultFragments(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:  diff.KindAddTable,
			Table: "events",
			Target: &schema.Table{
				Name: "events",
				Columns: []schema.Column{
					{Name: "id", Type: "BIGINT", PrimaryKey: true},
					{Name: "name", Type: "VARCHAR(255)", Nullable: false, DefaultValue: "''"},
					{Name: "comment_def", Type: "VARCHAR(255)", Nullable: false, DefaultValue: "'--'"},
					{Name: "created_at", Type: "TIMESTAMP WITH TIME ZONE", Nullable: false, DefaultValue: "CURRENT_TIMESTAMP"},
					{Name: "updated_at", Type: "TIMESTAMP(6) WITH TIME ZONE", Nullable: false, DefaultValue: "CURRENT_TIMESTAMP"},
				},
			},
		},
	}

	got, err := RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	if !strings.Contains(got[0], `"comment_def" VARCHAR(255) NOT NULL DEFAULT '--'`) {
		t.Fatalf("expected comment_def default to render, got %q", got[0])
	}
	if !strings.Contains(got[0], `"created_at" TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP`) {
		t.Fatalf("expected common type/default to render, got %q", got[0])
	}
	if !strings.Contains(got[0], `"updated_at" TIMESTAMP(6) WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP`) {
		t.Fatalf("expected timestamp precision with time zone to render, got %q", got[0])
	}
}

func TestRenderSQLRejectsUnsafeFragmentCommentAndTokenEdges(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "flag", Type: "TEXT", Nullable: true, DefaultValue: "''"},
		},
	}

	unsafeDefaults := []string{
		"'x' /* block */",
		"'x' */",
		"ALTER TABLE users",
		"DELETE FROM users",
		"INSERT INTO users VALUES (1)",
		"UNION SELECT 1",
		"TRUNCATE users",
	}
	for _, value := range unsafeDefaults {
		changes[0].Column.DefaultValue = value
		_, err := RenderSQL(changes, dialect.New("postgres"))
		if err == nil || !strings.Contains(err.Error(), "unsafe schema column default fragment") {
			t.Fatalf("RenderSQL error = %v, want unsafe fragment for %q", err, value)
		}
	}
}

func TestRenderSQLAllowsQuotedDangerousTokensInsideLiterals(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "flag", Type: "TEXT", Nullable: false, DefaultValue: "$$DELETE FROM users$$"},
		},
	}

	if _, err := RenderSQL(changes, dialect.New("postgres")); err != nil {
		t.Fatalf("expected dollar-quoted literal to pass, got %v", err)
	}
}

func TestRenderSQLAllowsMySQLSetColumnType(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "status", Type: "SET('a','b')", Nullable: false},
		},
	}

	got, err := RenderSQL(changes, dialect.New("mysql"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	if !strings.Contains(got[0], "`status` SET('a','b') NOT NULL") {
		t.Fatalf("expected mysql set type to render, got %q", got[0])
	}
}

func TestRenderSQLAllowsMySQLCharacterSetColumnType(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "users",
			Column: &schema.Column{Name: "name", Type: "VARCHAR(64) CHARACTER SET utf8mb4", Nullable: false},
		},
	}

	got, err := RenderSQL(changes, dialect.New("mysql"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	if !strings.Contains(got[0], "`name` VARCHAR(64) CHARACTER SET utf8mb4 NOT NULL") {
		t.Fatalf("expected mysql character set type to render, got %q", got[0])
	}
}

func TestRenderSQLAllowsPostgresEStringEscapedQuote(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  "events",
			Column: &schema.Column{Name: "payload", Type: "TEXT", Nullable: false, DefaultValue: `E'can\'t stop'`},
		},
	}

	got, err := RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	want := `ALTER TABLE "events" ADD COLUMN "payload" TEXT NOT NULL DEFAULT E'can\'t stop'`
	if got[0] != want {
		t.Fatalf("postgres SQL = %q, want %q", got[0], want)
	}
}

func TestRenderSQLRejectsUnsafeIdentifiers(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:   diff.KindAddColumn,
			Table:  `users"; DROP TABLE users; --`,
			Column: &schema.Column{Name: "email", Type: "TEXT"},
		},
	}

	_, err := RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema table identifier") {
		t.Fatalf("RenderSQL error = %v, want unsafe table identifier", err)
	}

	changes[0].Table = "users"
	changes[0].Column.Name = "bad-name"
	_, err = RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema column identifier") {
		t.Fatalf("RenderSQL error = %v, want unsafe column identifier", err)
	}

	changes[0].Column.Name = "users.email"
	_, err = RenderSQL(changes, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema column identifier") {
		t.Fatalf("RenderSQL error = %v, want dotted column identifier rejection", err)
	}

	indexChanges := diff.Changes{
		{
			Kind:  diff.KindAddIndex,
			Table: "users",
			Index: &schema.Index{Name: "public.idx_users_email", Columns: []string{"email"}},
		},
	}
	_, err = RenderSQL(indexChanges, dialect.New("sqlite"))
	if err == nil || !strings.Contains(err.Error(), "unsafe schema index identifier") {
		t.Fatalf("RenderSQL error = %v, want dotted index identifier rejection", err)
	}
}

func TestRenderDownSQL(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:  diff.KindAddTable,
			Table: "users",
			Target: &schema.Table{
				Name:    "users",
				Columns: []schema.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
			},
		},
		{
			Kind:   diff.KindAddColumn,
			Table:  "profiles",
			Column: &schema.Column{Name: "nickname", Type: "TEXT"},
		},
		{
			Kind:  diff.KindAddIndex,
			Table: "profiles",
			Index: &schema.Index{Name: "idx_profiles_nickname", Columns: []string{"nickname"}},
		},
	}

	got, err := RenderDownSQL(changes, dialect.New("sqlite"))
	if err != nil {
		t.Fatalf("RenderDownSQL failed: %v", err)
	}
	want := []string{
		`DROP INDEX "idx_profiles_nickname"`,
		`ALTER TABLE "profiles" DROP COLUMN "nickname"`,
		`DROP TABLE "users"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RenderDownSQL() = %#v, want %#v", got, want)
	}

	mysqlSQL, err := RenderDownSQL(changes[2:], dialect.New("mysql"))
	if err != nil {
		t.Fatalf("RenderDownSQL mysql failed: %v", err)
	}
	if mysqlSQL[0] != "DROP INDEX `idx_profiles_nickname` ON `profiles`" {
		t.Fatalf("mysql down SQL = %q", mysqlSQL[0])
	}

	postgresSQL, err := RenderDownSQL(diff.Changes{
		{
			Kind:  diff.KindAddIndex,
			Table: "public.profiles",
			Index: &schema.Index{Name: "idx_profiles_nickname", Columns: []string{"nickname"}},
		},
	}, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderDownSQL postgres failed: %v", err)
	}
	if postgresSQL[0] != `DROP INDEX "public"."idx_profiles_nickname"` {
		t.Fatalf("postgres down SQL = %q", postgresSQL[0])
	}

	sqliteQualifiedSQL, err := RenderDownSQL(diff.Changes{
		{
			Kind:  diff.KindAddIndex,
			Table: "tenant.profiles",
			Index: &schema.Index{Name: "idx_profiles_nickname", Columns: []string{"nickname"}},
		},
	}, dialect.New("sqlite"))
	if err != nil {
		t.Fatalf("RenderDownSQL sqlite qualified failed: %v", err)
	}
	if sqliteQualifiedSQL[0] != `DROP INDEX "tenant"."idx_profiles_nickname"` {
		t.Fatalf("sqlite qualified down SQL = %q", sqliteQualifiedSQL[0])
	}
}

func TestRenderSQLSupportsQualifiedTableNames(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind:  diff.KindAddTable,
			Table: "public.users",
			Target: &schema.Table{
				Schema: "public",
				Name:   "users",
				Columns: []schema.Column{
					{Name: "id", Type: "INTEGER", PrimaryKey: true},
				},
			},
		},
		{
			Kind:   diff.KindAddColumn,
			Table:  "public.users",
			Column: &schema.Column{Name: "email", Type: "TEXT", Nullable: true},
		},
	}

	got, err := RenderSQL(changes, dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	if got[0] != "CREATE TABLE \"public\".\"users\" (\n  \"id\" INTEGER NOT NULL,\n  PRIMARY KEY (\"id\")\n)" {
		t.Fatalf("create SQL = %q", got[0])
	}
	if got[1] != "ALTER TABLE \"public\".\"users\" ADD COLUMN \"email\" TEXT" {
		t.Fatalf("alter SQL = %q", got[1])
	}

	down, err := RenderDownSQL(changes[:1], dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderDownSQL failed: %v", err)
	}
	if down[0] != `DROP TABLE "public"."users"` {
		t.Fatalf("down SQL = %q", down[0])
	}
}

func TestRenderSQLSQLiteQualifiedTableIndexUsesSchemaOnIndexName(t *testing.T) {
	t.Parallel()

	changes := diff.Changes{
		{
			Kind: diff.KindAddTable,
			Target: &schema.Table{
				Schema: "tenant",
				Name:   "users",
				Columns: []schema.Column{
					{Name: "id", Type: "INTEGER", PrimaryKey: true},
					{Name: "email", Type: "TEXT", Nullable: true},
				},
				Indexes: []schema.Index{
					{Name: "idx_users_email", Columns: []string{"email"}},
				},
			},
		},
		{
			Kind:  diff.KindAddIndex,
			Table: "tenant.users",
			Index: &schema.Index{Name: "idx_users_id", Columns: []string{"id"}, Unique: true},
		},
	}

	got, err := RenderSQL(changes, dialect.New("sqlite"))
	if err != nil {
		t.Fatalf("RenderSQL failed: %v", err)
	}
	want := []string{
		"CREATE TABLE \"tenant\".\"users\" (\n  \"id\" INTEGER NOT NULL,\n  \"email\" TEXT,\n  PRIMARY KEY (\"id\")\n)",
		"CREATE INDEX \"tenant\".\"idx_users_email\" ON \"users\" (\"email\")",
		"CREATE UNIQUE INDEX \"tenant\".\"idx_users_id\" ON \"users\" (\"id\")",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RenderSQL() = %#v, want %#v", got, want)
	}

	postgresSQL, err := RenderSQL(changes[1:], dialect.New("postgres"))
	if err != nil {
		t.Fatalf("RenderSQL postgres failed: %v", err)
	}
	if postgresSQL[0] != `CREATE UNIQUE INDEX "idx_users_id" ON "tenant"."users" ("id")` {
		t.Fatalf("postgres SQL = %q", postgresSQL[0])
	}
}
