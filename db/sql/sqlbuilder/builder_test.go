package sqlbuilder

import (
	"context"
	"database/sql"
	stderrors "errors"
	"testing"

	"gochen/db"
	gerrors "gochen/errors"

	"gochen/testkit/require"
)

type execCall struct {
	query string
	args  []any
}

type fakeDB struct {
	dialectName string
	execCalls   []execCall
	execFunc    func(call execCall, n int) (sql.Result, error)
}

// DialectName 从存储中查询数据。
//
// 返回：
// - result：文本结果
func (f *fakeDB) DialectName() string { return f.dialectName }

// Query 从存储中查询对象。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - s：参数值（具体语义见函数上下文）（类型：string）
// - args：可变参数（类型：...any）
//
// 返回：
// - result1：返回结果（类型：core.IRows）
// - err：错误信息（nil 表示成功）
func (f *fakeDB) Query(context.Context, string, ...any) (db.IRows, error) {
	panic("fakeDB.Query: not implemented")
}

// QueryRow 从存储中查询对象。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - s：参数值（具体语义见函数上下文）（类型：string）
// - args：可变参数（类型：...any）
//
// 返回：
// - result：测试返回值（类型：core.IRow）
func (f *fakeDB) QueryRow(context.Context, string, ...any) db.IRow {
	panic("fakeDB.QueryRow: not implemented")
}

// Exec _：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - query：参数值（具体语义见函数上下文）（类型：string）
// - args：可变参数（用于补充配置/上下文）（类型：...any）
//
// 返回：
// - result1：返回结果（类型：stdsql.Result）
// - err：错误信息（nil 表示成功）
func (f *fakeDB) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	call := execCall{query: query, args: append([]any(nil), args...)}
	f.execCalls = append(f.execCalls, call)
	if f.execFunc != nil {
		return f.execFunc(call, len(f.execCalls))
	}
	return fakeResult{rows: 1}, nil
}

// Begin context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
//
// 返回：
// - result1：返回结果（类型：core.ITransaction）
// - err：错误信息（nil 表示成功）
func (f *fakeDB) Begin(context.Context) (db.ITransaction, error) {
	panic("fakeDB.Begin: not implemented")
}

// BeginTx 开启事务。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - txOptions：参数值（具体语义见函数上下文）（类型：*stdsql.TxOptions）
//
// 返回：
// - result1：返回结果（类型：core.ITransaction）
// - err：错误信息（nil 表示成功）
func (f *fakeDB) BeginTx(context.Context, *sql.TxOptions) (db.ITransaction, error) {
	panic("fakeDB.BeginTx: not implemented")
}

// Ping context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
//
// 返回：
// - err：错误信息（nil 表示成功）
func (f *fakeDB) Ping(context.Context) error { return nil }

// Close 关闭并释放资源。
//
// 返回：
// - err：错误信息（nil 表示成功）
func (f *fakeDB) Close() error { return nil }

type fakeResult struct{ rows int64 }

type fakeRowsAffectedErrResult struct{}

var _ ISQL = (*sqlImpl)(nil)

// LastInsertId result1：数值结果。
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r fakeResult) LastInsertId() (int64, error) { return 0, gerrors.New("not supported") }

// RowsAffected result1：数值结果。
//
// 返回：
// - err：错误信息（nil 表示成功）
func (r fakeResult) RowsAffected() (int64, error) { return r.rows, nil }

func (fakeRowsAffectedErrResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeRowsAffectedErrResult) RowsAffected() (int64, error) {
	return 0, stderrors.New("rows affected unavailable")
}

// TestSelectBuilder_Build_MySQLQuotesAndArgsOrder 验证 SelectBuilder Build MySQLQuotesAndArgsOrder。
func TestSelectBuilder_Build_MySQLQuotesAndArgsOrder(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select("id", "name").
		From("users").
		Where("age > ?", 18).
		Limit(10).
		Offset(5)

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, "SELECT `id`, `name` FROM `users` WHERE age > ? LIMIT ? OFFSET ?", q)
	require.Equal(t, []any{18, 10, 5}, args)
}

func TestSelectBuilder_OrFirstConditionExpandsSliceOnce(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)

	q, args, err := s.
		Select("*").
		From("users").
		Or("id IN (?)", []int{1, 2, 3}).
		Build()

	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `users` WHERE id IN (?, ?, ?)", q)
	require.Equal(t, []any{1, 2, 3}, args)
}

func TestSelectBuilder_WhereExpandsParenthesizedSliceOnce(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)

	q, args, err := s.
		Select("*").
		From("users").
		Where("id IN (?)", []int{1, 2, 3}).
		Build()

	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `users` WHERE id IN (?, ?, ?)", q)
	require.Equal(t, []any{1, 2, 3}, args)
}

func TestSelectBuilder_WhereExpandsBareSlicePlaceholder(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)

	q, args, err := s.
		Select("*").
		From("users").
		Where("id IN ?", []int{1, 2, 3}).
		Build()

	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `users` WHERE id IN (?, ?, ?)", q)
	require.Equal(t, []any{1, 2, 3}, args)
}

// TestSelectBuilder_GroupByRejectsUnsafe 验证 SelectBuilder GroupByRejectsUnsafe。
func TestSelectBuilder_GroupByRejectsUnsafe(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	_, _, err = s.Select("*").From("users").GroupBy("COUNT(1)").Build()
	require.Error(t, err)
	require.True(t, gerrors.Is(err, gerrors.InvalidInput))
}

// TestSelectBuilder_LimitOffsetNegativePanics 验证 SelectBuilder LimitOffsetNegativePanics。
func TestSelectBuilder_LimitOffsetNegativePanics(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)

	_, _, err = s.Select("*").From("users").Limit(-1).Build()
	require.Error(t, err)
	require.True(t, gerrors.Is(err, gerrors.InvalidInput))

	_, _, err = s.Select("*").From("users").Offset(-1).Build()
	require.Error(t, err)
	require.True(t, gerrors.Is(err, gerrors.InvalidInput))
}

// TestUpdateBuilder_SetMapDeterministicOrder 验证 UpdateBuilder SetMapDeterministicOrder。
func TestUpdateBuilder_SetMapDeterministicOrder(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Update("users").
		SetMap(map[string]any{"b": 2, "a": 1}).
		Where("id = ?", 9)

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, "UPDATE `users` SET `a` = ?, `b` = ? WHERE id = ?", q)
	require.Equal(t, []any{1, 2, 9}, args)
}

// TestInsertBuilder_Build_MultiRow 验证 InsertBuilder Build MultiRow。
func TestInsertBuilder_Build_MultiRow(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		InsertInto("users").
		Columns("id", "name").
		Values(1, "a").
		Values(2, "b")

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, "INSERT INTO `users` (`id`, `name`) VALUES (?, ?), (?, ?)", q)
	require.Equal(t, []any{1, "a", 2, "b"}, args)
}

// TestDeleteBuilder_LimitDialectSupport 验证 DeleteBuilder LimitDialectSupport。
//
// 方言不支持 `DELETE ... LIMIT` 时必须 fail-fast：静默丢弃 LIMIT 会把
// "最多删 N 行"悄悄放大成"删除全部匹配行"，属于不可逆的数据面缺陷。
func TestDeleteBuilder_LimitDialectSupport(t *testing.T) {
	t.Run("mysql supports delete limit", func(t *testing.T) {
		db := &fakeDB{dialectName: "mysql"}
		s, err := New(db)
		require.NoError(t, err)
		b := s.DeleteFrom("users").Where("id = ?", 1).Limit(10)
		q, args, err := b.Build()
		require.NoError(t, err)
		require.Equal(t, "DELETE FROM `users` WHERE id = ? LIMIT ?", q)
		require.Equal(t, []any{1, 10}, args)
	})

	for _, dialectName := range []string{"postgres", "sqlite"} {
		t.Run(dialectName+" rejects silent delete limit drop", func(t *testing.T) {
			db := &fakeDB{dialectName: dialectName}
			s, err := New(db)
			require.NoError(t, err)
			b := s.DeleteFrom("users").Where("id = ?", 1).Limit(10)
			q, args, buildErr := b.Build()
			require.Error(t, buildErr)
			require.ErrorIs(t, buildErr, gerrors.Unsupported)
			require.Equal(t, "", q)
			require.Nil(t, args)

			if _, err := b.Exec(context.Background()); err == nil {
				t.Fatal("expected Exec to fail instead of dropping LIMIT")
			}
			require.Len(t, db.execCalls, 0)
		})
	}

	t.Run("no limit keeps dialect specific sql", func(t *testing.T) {
		db := &fakeDB{dialectName: "sqlite"}
		s, err := New(db)
		require.NoError(t, err)
		q, args, err := s.DeleteFrom("users").Where("id = ?", 1).Build()
		require.NoError(t, err)
		require.Equal(t, `DELETE FROM "users" WHERE id = ?`, q)
		require.Equal(t, []any{1}, args)
	})
}

func TestUpsertBuilder_UpdateRowsAffectedErrorDoesNotSilentlySucceed(t *testing.T) {
	db := &fakeDB{
		dialectName: "mysql",
		execFunc: func(call execCall, n int) (sql.Result, error) {
			if n == 1 {
				return nil, stderrors.New("Error 1062: Duplicate entry")
			}
			return fakeRowsAffectedErrResult{}, nil
		},
	}
	s, err := New(db)
	require.NoError(t, err)

	_, err = s.UpsertInto("users").
		Columns("id", "name").
		Values(1, "alice").
		Key("id").
		Exec(context.Background())
	require.Error(t, err)
	require.True(t, gerrors.Is(err, gerrors.Database))
}

// TestUpsertBuilder_QuotesKeyColumnsInUpdateWhere 验证 UpsertBuilder QuotesKeyColumnsInUpdateWhere。
func TestUpsertBuilder_QuotesKeyColumnsInUpdateWhere(t *testing.T) {
	db := &fakeDB{
		dialectName: "mysql",
		execFunc: func(call execCall, n int) (sql.Result, error) {
			// 第一次执行为 INSERT，返回唯一键冲突；第二次为 UPDATE，返回 rows=1。
			if n == 1 {
				return nil, gerrors.New("Duplicate entry")
			}
			return fakeResult{rows: 1}, nil
		},
	}

	s, err := New(db)
	require.NoError(t, err)
	b := s.
		UpsertInto("users").
		Columns("id", "name").
		Values(1, "bob").
		Key("id")

	_, err = b.Exec(context.Background())
	require.NoError(t, err)
	require.Len(t, db.execCalls, 2)

	update := db.execCalls[1]
	require.Equal(t, "UPDATE `users` SET `name` = ? WHERE `id` = ?", update.query)
	require.Equal(t, []any{"bob", 1}, update.args)
}

// TestDeleteBuilder_NegativeLimitReturnsInvalidInput 验证 deleteBuilder.Limit(-1)
// 不会 panic，而是由 Build/Exec 返回 InvalidInput 错误。
//
// 回归 R2e-F02：该行为曾被 GoDoc 误述为“n<0 会 panic”，误导调用方以 recover 防御。
func TestDeleteBuilder_NegativeLimitReturnsInvalidInput(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)

	// 直接调用即可证明不 panic：若发生 panic，本用例会以崩溃失败。
	b := s.DeleteFrom("users").Limit(-1)
	q, args, err := b.Build()
	require.Error(t, err)
	require.True(t, gerrors.Is(err, gerrors.InvalidInput))
	require.Empty(t, q)
	require.Nil(t, args)

	if _, err := b.Exec(context.Background()); !gerrors.Is(err, gerrors.InvalidInput) {
		t.Fatalf("expected Exec to surface InvalidInput, got %v", err)
	}
	require.Len(t, db.execCalls, 0)
}

// TestSelectBuilder_InClauseExpandsSlice 验证 SelectBuilder InClauseExpandsSlice。
func TestSelectBuilder_InClauseExpandsSlice(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("users").
		Where("id IN ?", []int{1, 2, 3})

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `users` WHERE id IN (?, ?, ?)", q)
	require.Equal(t, []any{1, 2, 3}, args)
}

func TestSelectBuilder_NilArgRemainsScalarPlaceholder(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("users").
		Where("deleted_at IS ?", nil)

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `users` WHERE deleted_at IS ?", q)
	require.Equal(t, []any{nil}, args)
}

func TestPostgresWhereJSONQuestionOperatorDoesNotConsumePlaceholder(t *testing.T) {
	db := &fakeDB{dialectName: "postgres"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("events").
		Where("tenant_id = ? AND payload ? 'enabled'", "tenant-1")

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, `SELECT * FROM "events" WHERE tenant_id = ? AND payload ? 'enabled'`, q)
	require.Equal(t, []any{"tenant-1"}, args)
}

func TestPostgresWhereJSONQuestionOperatorWithPlaceholderValue(t *testing.T) {
	db := &fakeDB{dialectName: "postgres"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("events").
		Where("payload ? ? AND id = ?", "enabled", 42)

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, `SELECT * FROM "events" WHERE payload ? ? AND id = ?`, q)
	require.Equal(t, []any{"enabled", 42}, args)
}

func TestPostgresWhereJSONQuestionAnyAllOperatorsDoNotConsumePlaceholders(t *testing.T) {
	db := &fakeDB{dialectName: "postgres"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("events").
		Where("tenant_id = ? AND payload ?| array['a', 'b'] AND flags ?& array['x', 'y']", "tenant-1")

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, `SELECT * FROM "events" WHERE tenant_id = ? AND payload ?| array['a', 'b'] AND flags ?& array['x', 'y']`, q)
	require.Equal(t, []any{"tenant-1"}, args)
}

func TestPostgresWhereSkipsQuestionMarksInLiteralsAndComments(t *testing.T) {
	db := &fakeDB{dialectName: "postgres"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("events").
		Where("note = '?' AND $$ ? $$ = $$ ? $$ AND -- ? in comment\nid = ?", 7)

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM \"events\" WHERE note = '?' AND $$ ? $$ = $$ ? $$ AND -- ? in comment\nid = ?", q)
	require.Equal(t, []any{7}, args)
}

func TestPostgresUpdateDeleteWhereJSONQuestionOperator(t *testing.T) {
	db := &fakeDB{dialectName: "postgres"}
	s, err := New(db)
	require.NoError(t, err)

	updateQuery, updateArgs, err := s.
		Update("events").
		Set("status", "active").
		Where("tenant_id = ? AND payload ? 'enabled'", "tenant-1").
		Build()
	require.NoError(t, err)
	require.Equal(t, `UPDATE "events" SET "status" = ? WHERE tenant_id = ? AND payload ? 'enabled'`, updateQuery)
	require.Equal(t, []any{"active", "tenant-1"}, updateArgs)

	deleteQuery, deleteArgs, err := s.
		DeleteFrom("events").
		Where("payload ? ? AND id = ?", "enabled", 42).
		Build()
	require.NoError(t, err)
	require.Equal(t, `DELETE FROM "events" WHERE payload ? ? AND id = ?`, deleteQuery)
	require.Equal(t, []any{"enabled", 42}, deleteArgs)
}

func TestMySQLQuestionOperatorStillCountsAsPlaceholder(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	_, _, err = s.
		Select().
		From("events").
		Where("tenant_id = ? AND payload ? 'enabled'", "tenant-1").
		Build()
	require.Error(t, err)
	require.True(t, gerrors.Is(err, gerrors.InvalidInput))
}

// TestSelectBuilder_InClauseEmptySliceReturnsNoMatch 验证 SelectBuilder InClauseEmptySliceReturnsNoMatch。
func TestSelectBuilder_InClauseEmptySliceReturnsNoMatch(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("users").
		Where("id IN ?", []int{})

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `users` WHERE 0=1", q)
	require.Empty(t, args)
}

func TestSelectBuilder_EmptySliceReturnsNoMatchAcrossDialects(t *testing.T) {
	db := &fakeDB{dialectName: "postgres"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("users").
		Where("uuid IN ?", []string{})

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, `SELECT * FROM "users" WHERE 0=1`, q)
	require.Empty(t, args)
}

func TestSelectBuilder_NotInEmptySliceKeepsLegacyNoMatchBehavior(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		Select().
		From("users").
		Where("id NOT IN ?", []int{})

	q, args, err := b.Build()
	require.NoError(t, err)
	require.Equal(t, "SELECT * FROM `users` WHERE 0=1", q)
	require.Empty(t, args)
}

// TestUpsertBuilder_UnsafeKeyReturnsInvalidInput 验证 UpsertBuilder UnsafeKeyReturnsInvalidInput。
func TestUpsertBuilder_UnsafeKeyReturnsInvalidInput(t *testing.T) {
	db := &fakeDB{dialectName: "mysql"}
	s, err := New(db)
	require.NoError(t, err)
	b := s.
		UpsertInto("users").
		Columns("id", "name").
		Values(1, "bob").
		Key("id;drop")

	_, err = b.Exec(context.Background())
	require.Error(t, err)
	require.True(t, gerrors.Is(err, gerrors.InvalidInput))
}
