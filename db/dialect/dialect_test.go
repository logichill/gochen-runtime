package dialect

import (
	"context"
	"database/sql"
	coredialect "gochen/db/dialect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"gochen/db"
)

// TestRebind_Postgres 验证 Rebind Postgres。
func TestRebind_Postgres(t *testing.T) {
	d := New("postgres")
	q := "SELECT * FROM t WHERE a = ? AND b IN (?, ?)"
	got := d.Rebind(q)
	want := "SELECT * FROM t WHERE a = $1 AND b IN ($2, $3)"
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresSkipsLiteralsCommentsAndJSONQuestionOperators(t *testing.T) {
	d := New("postgres")
	q := `SELECT payload ? 'name',
       payload ?| array['a', 'b'],
       payload ?& array['a', 'b'],
       note = '?',
       $$ ? $$,
       -- ? in comment
       id = ?
FROM events
WHERE tenant_id = ? AND payload ? 'enabled' AND (? IS NULL OR status = ?)`
	got := d.Rebind(q)
	want := `SELECT payload ? 'name',
       payload ?| array['a', 'b'],
       payload ?& array['a', 'b'],
       note = '?',
       $$ ? $$,
       -- ? in comment
       id = $1
FROM events
WHERE tenant_id = $2 AND payload ? 'enabled' AND ($3 IS NULL OR status = $4)`
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresJSONQuestionOperatorWithPlaceholderValue(t *testing.T) {
	d := New("postgres")
	q := `SELECT * FROM events WHERE payload ? ? AND id = ?`
	got := d.Rebind(q)
	want := `SELECT * FROM events WHERE payload ? $1 AND id = $2`
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresQuestionPlaceholdersNearKeywords(t *testing.T) {
	d := New("postgres")
	q := `SELECT * FROM users WHERE name LIKE ? AND deleted_at IS ? AND id = ?`
	got := d.Rebind(q)
	want := `SELECT * FROM users WHERE name LIKE $1 AND deleted_at IS $2 AND id = $3`
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresSkipsEscapeStringLiterals(t *testing.T) {
	d := New("postgres")
	q := `SELECT E'it\'s ?' AS note, U&'d\0061t?' AS escaped, id = ?`
	got := d.Rebind(q)
	want := `SELECT E'it\'s ?' AS note, U&'d\0061t?' AS escaped, id = $1`
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresHandlesQualifiedNamesAroundPrefixedStrings(t *testing.T) {
	d := New("postgres")
	q := `SELECT schema.payload ? 'name', table.E'it\'s ?' AS note, id = ?`
	got := d.Rebind(q)
	want := `SELECT schema.payload ? 'name', table.E'it\'s ?' AS note, id = $1`
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresSkipsBitAndHexStringLiterals(t *testing.T) {
	d := New("postgres")
	q := `SELECT B'10?1' AS bits, X'AB?C' AS hex, id = ?`
	got := d.Rebind(q)
	want := `SELECT B'10?1' AS bits, X'AB?C' AS hex, id = $1`
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresSkipsCommentsAroundJSONQuestionOperator(t *testing.T) {
	d := New("postgres")
	q := `SELECT *
FROM events
WHERE payload /* json exists */ ? 'name'
  AND payload -- still JSON operator
  ? 'enabled'
  AND id = ?`
	got := d.Rebind(q)
	want := `SELECT *
FROM events
WHERE payload /* json exists */ ? 'name'
  AND payload -- still JSON operator
  ? 'enabled'
  AND id = $1`
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresSkipsNestedBlockComments(t *testing.T) {
	d := New("postgres")
	q := `SELECT 1 /* outer /* inner ? */ still comment ? */, id = ?`
	got := d.Rebind(q)
	want := `SELECT 1 /* outer /* inner ? */ still comment ? */, id = $1`
	if got != want {
		t.Fatalf("Rebind mismatch\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRebind_PostgresLargeParameterizedInsert(t *testing.T) {
	d := New("postgres")
	var query strings.Builder
	var want strings.Builder
	query.WriteString("INSERT INTO events (id, payload) VALUES ")
	want.WriteString("INSERT INTO events (id, payload) VALUES ")
	argIndex := 1
	for i := 0; i < 512; i++ {
		if i > 0 {
			query.WriteString(", ")
			want.WriteString(", ")
		}
		query.WriteString("(?, ?)")
		want.WriteString("($")
		want.WriteString(strconv.Itoa(argIndex))
		want.WriteString(", $")
		want.WriteString(strconv.Itoa(argIndex + 1))
		want.WriteByte(')')
		argIndex += 2
	}
	if got := d.Rebind(query.String()); got != want.String() {
		t.Fatalf("Rebind mismatch on large insert")
	}
}

// TestRebind_NoChangeForMySQLSQLite 验证 Rebind NoChangeForMySQLSQLite。
func TestRebind_NoChangeForMySQLSQLite(t *testing.T) {
	tests := []struct {
		name string
		d    coredialect.IDialect
	}{
		{"mysql", New("mysql")},
		{"sqlite", New("sqlite")},
		{"unknown", New("unknown")},
	}

	orig := "DELETE FROM t WHERE id = ? AND name = ?"
	for _, tt := range tests {
		if got := tt.d.Rebind(orig); got != orig {
			t.Fatalf("%s: expected no change, got %s", tt.name, got)
		}
	}
}

func TestDialectSupportsSavepoints(t *testing.T) {
	tests := []struct {
		name string
		d    coredialect.IDialect
		want bool
	}{
		{name: "mysql", d: New("mysql"), want: true},
		{name: "sqlite", d: New("sqlite"), want: true},
		{name: "postgres", d: New("postgres"), want: true},
		{name: "unknown", d: New("sqlserver"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.d.SupportsSavepoints(); got != tt.want {
				t.Fatalf("SupportsSavepoints() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDialectSupportsDeleteLimitMatchesDriverCompileCapability 验证
// SupportsDeleteLimit 按驱动编译期能力报告：锁定的 SQLite 驱动未启用
// SQLITE_ENABLE_UPDATE_DELETE_LIMIT，必须为 false（历史缺陷是对 SQLite
// 误报 true，导致生成 SQLite 无法解析的 `DELETE ... LIMIT` 语句）。
func TestDialectSupportsDeleteLimitMatchesDriverCompileCapability(t *testing.T) {
	tests := []struct {
		name string
		d    coredialect.IDialect
		want bool
	}{
		{name: "mysql", d: New("mysql"), want: true},
		{name: "sqlite", d: New("sqlite"), want: false},
		{name: "sqlite3 alias", d: New("sqlite3"), want: false},
		{name: "postgres", d: New("postgres"), want: false},
		{name: "unknown", d: New("sqlserver"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.d.SupportsDeleteLimit(); got != tt.want {
				t.Fatalf("SupportsDeleteLimit() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStandardDialectSelectLockClause(t *testing.T) {
	tests := []struct {
		name       string
		dialect    coredialect.IDialect
		skipLocked bool
		want       string
	}{
		{name: "mysql", dialect: New("mysql"), want: " FOR UPDATE"},
		{name: "mysql skip locked", dialect: New("mysql"), skipLocked: true, want: " FOR UPDATE SKIP LOCKED"},
		{name: "postgres", dialect: New("postgres"), want: " FOR UPDATE"},
		{name: "postgres skip locked", dialect: New("postgres"), skipLocked: true, want: " FOR UPDATE SKIP LOCKED"},
		{name: "sqlite no-op", dialect: New("sqlite"), skipLocked: true},
		{name: "unknown no-op", dialect: New("unknown"), skipLocked: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.dialect.SelectLockClause(tt.skipLocked)
			if err != nil {
				t.Fatalf("SelectLockClause() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("SelectLockClause() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestQuoteIdentifierEscapesEmbeddedQuotes(t *testing.T) {
	tests := []struct {
		name string
		d    coredialect.IDialect
		in   string
		want string
	}{
		{name: "mysql", d: New("mysql"), in: "tenant`table.id", want: "`tenant``table`.`id`"},
		{name: "sqlite", d: New("sqlite"), in: `tenant"table.id`, want: `"tenant""table"."id"`},
		{name: "postgres", d: New("postgres"), in: `tenant"table.id`, want: `"tenant""table"."id"`},
		{name: "unknown", d: New("sqlserver"), in: `tenant"table.id`, want: `tenant"table.id`},
	}
	for _, tt := range tests {
		if got := tt.d.QuoteIdentifier(tt.in); got != tt.want {
			t.Fatalf("%s: want %s, got %s", tt.name, tt.want, got)
		}
	}
}

type dialectProviderDB struct {
	dialect string
}

func (d *dialectProviderDB) DialectName() string { return d.dialect }

func (d *dialectProviderDB) Query(context.Context, string, ...any) (db.IRows, error) {
	return nil, nil
}
func (d *dialectProviderDB) QueryRow(context.Context, string, ...any) db.IRow { return nil }
func (d *dialectProviderDB) Exec(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}
func (d *dialectProviderDB) Begin(context.Context) (db.ITransaction, error) { return nil, nil }
func (d *dialectProviderDB) BeginTx(context.Context, *sql.TxOptions) (db.ITransaction, error) {
	return nil, nil
}
func (d *dialectProviderDB) Ping(context.Context) error { return nil }
func (d *dialectProviderDB) Close() error               { return nil }

type countingDialectProviderDB struct {
	dialectProviderDB
	calls atomic.Int64
}

func (d *countingDialectProviderDB) DialectName() string {
	d.calls.Add(1)
	return d.dialect
}

type transactionDialectProviderDB struct {
	countingDialectProviderDB
}

func (*transactionDialectProviderDB) Commit() error   { return nil }
func (*transactionDialectProviderDB) Rollback() error { return nil }

type capabilityDialect struct {
	coredialect.IDialect
}

func (capabilityDialect) Name() coredialect.Name { return coredialect.Name("custom") }

func (capabilityDialect) QuoteIdentifier(name string) string { return "custom(" + name + ")" }

type capabilityDialectProviderDB struct {
	dialectProviderDB
	dialect coredialect.IDialect
}

func (d *capabilityDialectProviderDB) Dialect() coredialect.IDialect { return d.dialect }

type embeddedDialectDB struct {
	db.IDatabase
}

type explicitUnknownDialectDB struct {
	db.IDatabase
}

func (d *explicitUnknownDialectDB) DialectName() string { return "unknown" }

type unwrapDialectDB struct {
	inner db.IDatabase
}

func (d unwrapDialectDB) UnwrapDatabase() db.IDatabase { return d.inner }
func (d unwrapDialectDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	return d.inner.Query(ctx, query, args...)
}
func (d unwrapDialectDB) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	return d.inner.QueryRow(ctx, query, args...)
}
func (d unwrapDialectDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.inner.Exec(ctx, query, args...)
}
func (d unwrapDialectDB) Begin(ctx context.Context) (db.ITransaction, error) {
	return d.inner.Begin(ctx)
}
func (d unwrapDialectDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	return d.inner.BeginTx(ctx, opts)
}
func (d unwrapDialectDB) Ping(ctx context.Context) error { return d.inner.Ping(ctx) }
func (d unwrapDialectDB) Close() error                   { return d.inner.Close() }

func TestFromDatabaseUnwrapsDatabaseWrappers(t *testing.T) {
	base := &dialectProviderDB{dialect: "sqlite"}
	tests := []struct {
		name string
		db   db.IDatabase
	}{
		{name: "direct", db: base},
		{name: "embedded", db: &embeddedDialectDB{IDatabase: base}},
		{name: "unwrap", db: unwrapDialectDB{inner: base}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromDatabase(tt.db).Name(); got != coredialect.NameSQLite {
				t.Fatalf("FromDatabase() = %q, want %q", got, coredialect.NameSQLite)
			}
		})
	}
}

func TestFromDatabaseRespectsExplicitUnknownDialect(t *testing.T) {
	base := &dialectProviderDB{dialect: "sqlite"}
	wrapped := &explicitUnknownDialectDB{IDatabase: base}
	if got := FromDatabase(wrapped).Name(); got != coredialect.NameUnknown {
		t.Fatalf("FromDatabase() = %q, want explicit unknown", got)
	}
}

func TestFromDatabaseCachesRecognizedDialect(t *testing.T) {
	db := &countingDialectProviderDB{dialectProviderDB: dialectProviderDB{dialect: "sqlite"}}

	if got := FromDatabase(db).Name(); got != coredialect.NameSQLite {
		t.Fatalf("FromDatabase() = %q, want %q", got, coredialect.NameSQLite)
	}
	if got := FromDatabase(db).Name(); got != coredialect.NameSQLite {
		t.Fatalf("FromDatabase() = %q, want %q", got, coredialect.NameSQLite)
	}
	if calls := db.calls.Load(); calls != 1 {
		t.Fatalf("DialectName calls = %d, want 1", calls)
	}
}

func TestFromDatabaseDoesNotCacheTransactions(t *testing.T) {
	before := dialectCacheEntryCount()

	for range 100 {
		tx := &transactionDialectProviderDB{
			countingDialectProviderDB: countingDialectProviderDB{
				dialectProviderDB: dialectProviderDB{dialect: "sqlite"},
			},
		}
		if got := FromDatabase(tx).Name(); got != coredialect.NameSQLite {
			t.Fatalf("FromDatabase() = %q, want %q", got, coredialect.NameSQLite)
		}
		if calls := tx.calls.Load(); calls != 1 {
			t.Fatalf("DialectName calls = %d, want 1", calls)
		}
	}

	if after := dialectCacheEntryCount(); after != before {
		t.Fatalf("dialect cache entries = %d, want unchanged %d", after, before)
	}
}

func dialectCacheEntryCount() int {
	count := 0
	databaseDialectCache.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func TestFromDatabasePrefersDialectCapabilityProvider(t *testing.T) {
	provided := capabilityDialect{IDialect: New("postgres")}
	db := &capabilityDialectProviderDB{
		dialectProviderDB: dialectProviderDB{dialect: "mysql"},
		dialect:           provided,
	}

	got := FromDatabase(db)
	if got.Name() != coredialect.Name("custom") {
		t.Fatalf("FromDatabase() name = %q, want custom", got.Name())
	}
	if quoted := got.QuoteIdentifier("events"); quoted != "custom(events)" {
		t.Fatalf("QuoteIdentifier() = %q, want custom(events)", quoted)
	}
}

func TestResolveMaxBindParameters(t *testing.T) {
	// 未配置（含负值）一律取默认，绝不返回一个会把语句切碎的小数字。
	if got := ResolveMaxBindParameters(0); got != DefaultMaxBindParameters {
		t.Fatalf("expected default for 0, got %d", got)
	}
	if got := ResolveMaxBindParameters(-1); got != DefaultMaxBindParameters {
		t.Fatalf("expected default for -1, got %d", got)
	}
	if got := ResolveMaxBindParameters(999); got != 999 {
		t.Fatalf("expected configured value, got %d", got)
	}
}

func TestValidateMaxBindParameters(t *testing.T) {
	// 0 表示"用默认值"，是合法配置。
	if err := ValidateMaxBindParameters(0); err != nil {
		t.Fatalf("expected 0 to be valid: %v", err)
	}
	if err := ValidateMaxBindParameters(MinMaxBindParameters); err != nil {
		t.Fatalf("expected lower bound to be valid: %v", err)
	}
	// 低于下界会静默把每条批量语句切碎，必须在构造期报错。
	if err := ValidateMaxBindParameters(MinMaxBindParameters - 1); err == nil {
		t.Fatal("expected below-minimum budget to be rejected")
	}
	if err := ValidateMaxBindParameters(-1); err == nil {
		t.Fatal("expected negative budget to be rejected")
	}
}
