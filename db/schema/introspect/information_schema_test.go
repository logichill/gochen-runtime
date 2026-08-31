package introspect

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"gochen/db"
)

func TestMySQLIntrospector(t *testing.T) {
	t.Parallel()

	database := &fakeInspectDB{
		dialect: "mysql",
		schema:  "appdb",
		tables:  []string{"users"},
		columns: map[string][][]any{
			"users": {
				{"id", "bigint", "NO", nullString(""), "PRI", "auto_increment"},
				{"email", "varchar(128)", "NO", nullString(""), "", ""},
				{"name", "text", "YES", sql.NullString{String: "", Valid: true}, "", ""},
			},
		},
		indexes: map[string][][]any{
			"users": {
				{"idx_users_email", 0, nullString("email"), 1, nullInt(0), nullString("A")},
				{"idx_users_email_expr", 1, nullString(""), 1, nullInt(0), nullString("")},
				{"idx_users_mixed_expr", 1, nullString(""), 1, nullInt(0), nullString("")},
				{"idx_users_mixed_expr", 1, nullString("name"), 2, nullInt(0), nullString("A")},
				{"idx_users_email_prefix", 1, nullString("email"), 1, sql.NullInt64{Int64: 16, Valid: true}, nullString("A")},
			},
		},
	}

	got, err := NewMySQL().Inspect(context.Background(), database)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	users, ok := got.FindTable("appdb", "users")
	if !ok {
		t.Fatal("expected users table")
	}
	if users.Schema != "appdb" {
		t.Fatalf("users schema = %q, want appdb", users.Schema)
	}
	id, ok := users.FindColumn("id")
	if !ok || !id.PrimaryKey || !id.AutoIncrement || id.Nullable {
		t.Fatalf("unexpected id column: %#v ok=%v", id, ok)
	}
	email, ok := users.FindColumn("email")
	if !ok || email.Type != "varchar(128)" || email.Nullable {
		t.Fatalf("unexpected email column: %#v ok=%v", email, ok)
	}
	name, ok := users.FindColumn("name")
	if !ok || name.DefaultValue != "''" {
		t.Fatalf("unexpected name column default: %#v ok=%v", name, ok)
	}
	index, ok := users.FindIndex("idx_users_email")
	if !ok || !index.Unique || len(index.Columns) != 1 || index.Columns[0] != "email" {
		t.Fatalf("unexpected index: %#v ok=%v", index, ok)
	}
	if idx, ok := users.FindIndex("idx_users_email_expr"); !ok || !idx.Unsupported {
		t.Fatalf("expected expression index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if idx, ok := users.FindIndex("idx_users_mixed_expr"); !ok || !idx.Unsupported {
		t.Fatalf("expected mixed expression index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if idx, ok := users.FindIndex("idx_users_email_prefix"); !ok || !idx.Unsupported {
		t.Fatalf("expected prefix index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "idx_users_email_prefix") {
		t.Fatalf("expected complex index warning, got %#v", got.Warnings)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("expected introspection warnings")
	}
}

func TestPostgresIntrospector(t *testing.T) {
	t.Parallel()

	database := &fakeInspectDB{
		dialect: "postgres",
		schema:  "public",
		tables:  []string{"users"},
		columns: map[string][][]any{
			"users": {
				{"id", "bigint", nullString("int8"), nullString("pg_catalog"), nullString(""), nullString(""), "public", nullInt(0), nullInt(64), nullInt(0), "NO", nullString(""), "YES", true},
				{"email", "character varying", nullString("varchar"), nullString("pg_catalog"), nullString(""), nullString(""), "public", nullInt(128), nullInt(0), nullInt(0), "NO", nullString(""), "NO", false},
				{"name", "text", nullString("text"), nullString("pg_catalog"), nullString(""), nullString(""), "public", nullInt(0), nullInt(0), nullInt(0), "YES", nullString("'anonymous'::text"), "NO", false},
				{"sequence_value", "bigint", nullString("int8"), nullString("pg_catalog"), nullString(""), nullString(""), "public", nullInt(0), nullInt(64), nullInt(0), "NO", nullString("nextval('users_sequence_value_seq'::regclass)"), "NO", false},
				{"amount", "numeric", nullString("numeric"), nullString("pg_catalog"), nullString(""), nullString(""), "public", nullInt(0), nullInt(10), sql.NullInt64{Int64: 0, Valid: true}, "NO", nullString("0"), "NO", false},
				{"tags", "ARRAY", nullString("_text"), nullString("pg_catalog"), nullString(""), nullString(""), "public", nullInt(0), nullInt(0), nullInt(0), "YES", nullString(""), "NO", false},
				{"status_history", "ARRAY", nullString("_user_status"), nullString("public"), nullString(""), nullString(""), "public", nullInt(0), nullInt(0), nullInt(0), "YES", nullString(""), "NO", false},
				{"shared_codes", "ARRAY", nullString("_billing_code"), nullString("shared"), nullString(""), nullString(""), "public", nullInt(0), nullInt(0), nullInt(0), "YES", nullString(""), "NO", false},
				{"status", "USER-DEFINED", nullString("user_status"), nullString("public"), nullString(""), nullString(""), "public", nullInt(0), nullInt(0), nullInt(0), "NO", nullString("'active'::user_status"), "NO", false},
				{"tenant_id", "bigint", nullString("int8"), nullString("pg_catalog"), nullString("tenant_id_domain"), nullString("public"), "public", nullInt(0), nullInt(64), nullInt(0), "NO", nullString(""), "NO", false},
				{"billing_code", "USER-DEFINED", nullString("billing_code"), nullString("shared"), nullString(""), nullString(""), "public", nullInt(0), nullInt(0), nullInt(0), "NO", nullString(""), "NO", false},
				{"external_tenant_id", "bigint", nullString("int8"), nullString("pg_catalog"), nullString("tenant_id_domain"), nullString("shared"), "public", nullInt(0), nullInt(64), nullInt(0), "NO", nullString(""), "NO", false},
			},
		},
		indexes: map[string][][]any{
			"users": {
				{"idx_users_email", 0, nullString("email"), 1, "email", 1, 1, false, false},
				{"idx_users_email_desc", 1, nullString("email"), 1, "email DESC", 1, 1, false, false},
				{"idx_users_email_include", 1, nullString("email"), 1, "email", 1, 2, false, false},
				{"idx_users_email_partial", 1, nullString("email"), 1, "email", 1, 1, true, false},
				{"idx_users_lower_email", 1, nullString(""), 1, "lower(email)", 1, 1, false, true},
			},
		},
	}

	got, err := NewPostgres().Inspect(context.Background(), database)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	users, ok := got.FindTable("public", "users")
	if !ok {
		t.Fatal("expected users table")
	}
	if users.Schema != "public" {
		t.Fatalf("users schema = %q, want public", users.Schema)
	}
	email, ok := users.FindColumn("email")
	if !ok || email.Type != "VARCHAR(128)" || email.Nullable {
		t.Fatalf("unexpected email column: %#v ok=%v", email, ok)
	}
	id, ok := users.FindColumn("id")
	if !ok || !id.PrimaryKey || !id.AutoIncrement || id.DefaultValue != "" {
		t.Fatalf("unexpected id column: %#v ok=%v", id, ok)
	}
	name, ok := users.FindColumn("name")
	if !ok || name.DefaultValue != "'anonymous'" {
		t.Fatalf("unexpected name column default: %#v ok=%v", name, ok)
	}
	sequenceValue, ok := users.FindColumn("sequence_value")
	if !ok || sequenceValue.PrimaryKey || !sequenceValue.AutoIncrement || sequenceValue.DefaultValue != "" {
		t.Fatalf("unexpected sequence_value column: %#v ok=%v", sequenceValue, ok)
	}
	amount, ok := users.FindColumn("amount")
	if !ok || amount.Type != "NUMERIC(10,0)" || amount.DefaultValue != "0" {
		t.Fatalf("unexpected amount column: %#v ok=%v", amount, ok)
	}
	tags, ok := users.FindColumn("tags")
	if !ok || tags.Type != "TEXT[]" {
		t.Fatalf("unexpected tags column: %#v ok=%v", tags, ok)
	}
	statusHistory, ok := users.FindColumn("status_history")
	if !ok || statusHistory.Type != "user_status[]" {
		t.Fatalf("unexpected status_history column: %#v ok=%v", statusHistory, ok)
	}
	sharedCodes, ok := users.FindColumn("shared_codes")
	if !ok || sharedCodes.Type != "shared.billing_code[]" {
		t.Fatalf("unexpected shared_codes column: %#v ok=%v", sharedCodes, ok)
	}
	status, ok := users.FindColumn("status")
	if !ok || status.Type != "user_status" || status.DefaultValue != "'active'" {
		t.Fatalf("unexpected status column: %#v ok=%v", status, ok)
	}
	tenantID, ok := users.FindColumn("tenant_id")
	if !ok || tenantID.Type != "tenant_id_domain" {
		t.Fatalf("unexpected tenant_id column: %#v ok=%v", tenantID, ok)
	}
	billingCode, ok := users.FindColumn("billing_code")
	if !ok || billingCode.Type != "shared.billing_code" {
		t.Fatalf("unexpected billing_code column: %#v ok=%v", billingCode, ok)
	}
	externalTenantID, ok := users.FindColumn("external_tenant_id")
	if !ok || externalTenantID.Type != "shared.tenant_id_domain" {
		t.Fatalf("unexpected external_tenant_id column: %#v ok=%v", externalTenantID, ok)
	}
	index, ok := users.FindIndex("idx_users_email")
	if !ok || !index.Unique || len(index.Columns) != 1 || index.Columns[0] != "email" {
		t.Fatalf("unexpected index: %#v ok=%v", index, ok)
	}
	if idx, ok := users.FindIndex("idx_users_email_desc"); !ok || !idx.Unsupported {
		t.Fatalf("expected descending index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if idx, ok := users.FindIndex("idx_users_email_include"); !ok || !idx.Unsupported {
		t.Fatalf("expected include index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if idx, ok := users.FindIndex("idx_users_email_partial"); !ok || !idx.Unsupported {
		t.Fatalf("expected partial index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	if idx, ok := users.FindIndex("idx_users_lower_email"); !ok || !idx.Unsupported {
		t.Fatalf("expected expression index to be marked unsupported, got %#v ok=%v", idx, ok)
	}
	queries := strings.Join(database.queries, "\n")
	if strings.Contains(queries, "ix.indpred is null") {
		t.Fatal("expected postgres introspector not to filter out partial indexes")
	}
	if strings.Contains(queries, "ix.indexprs is null") {
		t.Fatal("expected postgres introspector not to filter out expression indexes")
	}
	if !strings.Contains(queries, "ix.indpred is not null") {
		t.Fatal("expected postgres introspector to expose partial index metadata")
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "idx_users_email_include") {
		t.Fatalf("expected complex index warning, got %#v", got.Warnings)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("expected introspection warnings")
	}
}

type fakeInspectDB struct {
	dialect string
	schema  string
	tables  []string
	columns map[string][][]any
	indexes map[string][][]any
	queries []string
}

func (f *fakeInspectDB) Query(_ context.Context, query string, args ...any) (db.IRows, error) {
	query = strings.ToLower(query)
	f.queries = append(f.queries, query)
	switch {
	case strings.Contains(query, "information_schema.tables"):
		rows := make([][]any, 0, len(f.tables))
		for _, table := range f.tables {
			rows = append(rows, []any{f.schema, table})
		}
		return &fakeInspectRows{rows: rows}, nil
	case strings.Contains(query, "information_schema.columns"):
		return &fakeInspectRows{rows: f.columns[args[0].(string)]}, nil
	case strings.Contains(query, "information_schema.statistics") || strings.Contains(query, "pg_index"):
		return &fakeInspectRows{rows: f.indexes[args[0].(string)]}, nil
	default:
		return &fakeInspectRows{}, nil
	}
}

func (f *fakeInspectDB) QueryRow(context.Context, string, ...any) db.IRow {
	return fakeInspectRow{}
}

func (f *fakeInspectDB) Exec(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}

func (f *fakeInspectDB) Begin(context.Context) (db.ITransaction, error) {
	return nil, nil
}

func (f *fakeInspectDB) BeginTx(context.Context, *sql.TxOptions) (db.ITransaction, error) {
	return nil, nil
}

func (f *fakeInspectDB) Ping(context.Context) error { return nil }

func (f *fakeInspectDB) Close() error { return nil }

func (f *fakeInspectDB) DialectName() string { return f.dialect }

type fakeInspectRows struct {
	rows [][]any
	pos  int
}

func (r *fakeInspectRows) Next() bool {
	if r.pos >= len(r.rows) {
		return false
	}
	r.pos++
	return true
}

func (r *fakeInspectRows) Scan(dest ...any) error {
	row := r.rows[r.pos-1]
	for i := range dest {
		switch d := dest[i].(type) {
		case *string:
			*d = row[i].(string)
		case *int:
			*d = row[i].(int)
		case *sql.NullString:
			switch value := row[i].(type) {
			case sql.NullString:
				*d = value
			case string:
				*d = nullString(value)
			}
		case *sql.NullInt64:
			*d = row[i].(sql.NullInt64)
		case *bool:
			*d = row[i].(bool)
		}
	}
	return nil
}

func (r *fakeInspectRows) Close() error { return nil }

func (r *fakeInspectRows) Err() error { return nil }

func (r *fakeInspectRows) Columns() ([]string, error) { return nil, nil }

func (r *fakeInspectRows) ColumnTypes() ([]*sql.ColumnType, error) { return nil, nil }

type fakeInspectRow struct{}

func (fakeInspectRow) Scan(...any) error { return nil }

func (fakeInspectRow) Err() error { return nil }

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullInt(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: value != 0}
}
