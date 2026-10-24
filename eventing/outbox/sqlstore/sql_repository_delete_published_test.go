package sqlstore

import (
	"context"
	"database/sql"
	stdErrors "errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"gochen/testkit/assert"
	"gochen/testkit/require"

	rtdialect "gochen-runtime/db/dialect"

	"gochen/db"
	"gochen/errors"
	"gochen/eventing/outbox"
	"gochen/observe/logging"
)

// —— P2#26（fingerprint 28876db2）修复验证：DeletePublished 分批删除的 keyset
// 游标、批边界与 RowsAffected 异常防护 ——

// captureQueryDB 记录经过的 SELECT 查询与参数，其余调用透传。
type captureQueryDB struct {
	db.IDatabase
	mu      sync.Mutex
	queries []capturedQuery
}

type capturedQuery struct {
	query string
	args  []any
}

func (d *captureQueryDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	d.mu.Lock()
	d.queries = append(d.queries, capturedQuery{query: query, args: append([]any(nil), args...)})
	d.mu.Unlock()
	return d.IDatabase.Query(ctx, query, args...)
}

func (d *captureQueryDB) selectCalls() []capturedQuery {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []capturedQuery
	for _, q := range d.queries {
		if strings.Contains(q.query, "SELECT id FROM") {
			out = append(out, q)
		}
	}
	return out
}

// zeroAffectedNoExecDB 模拟 RowsAffected 恒 0 的异常驱动：DELETE 语句不实际
// 执行（保证“账目为 0”与表内数据一致），返回 0 受影响行。
type zeroAffectedNoExecDB struct {
	db.IDatabase
}

type zeroAffectedResult struct{}

func (zeroAffectedResult) LastInsertId() (int64, error) { return 0, stdErrors.New("not supported") }
func (zeroAffectedResult) RowsAffected() (int64, error) { return 0, nil }

func (d zeroAffectedNoExecDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(query)), "DELETE") {
		return zeroAffectedResult{}, nil
	}
	return d.IDatabase.Exec(ctx, query, args...)
}

// insertOutboxRows 直接批量插入指定状态的 outbox 行，返回写入行数。
func insertOutboxRows(tb testing.TB, database db.IDatabase, table string, label string, count int, status outbox.OutboxStatus, publishedAt *time.Time, createdAt time.Time) {
	tb.Helper()
	const chunk = 200
	for start := 0; start < count; start += chunk {
		end := start + chunk
		if end > count {
			end = count
		}
		var sb strings.Builder
		args := make([]any, 0, (end-start)*5)
		sb.WriteString(fmt.Sprintf(
			`INSERT INTO %s (aggregate_id, aggregate_type, event_id, event_type, event_data, status, claim_token, created_at, published_at, retry_count) VALUES `,
			table,
		))
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteString(", ")
			}
			sb.WriteString("(?, ?, ?, ?, ?, ?, '', ?, ?, 0)")
			args = append(args,
				1, "TestAggregate",
				fmt.Sprintf("bulk-%s-event-%d", label, i),
				"TestEvent", "{}",
				status,
				createdAt,
			)
			if publishedAt != nil {
				args = append(args, *publishedAt)
			} else {
				args = append(args, nil)
			}
		}
		_, err := database.Exec(context.Background(), sb.String(), args...)
		require.NoError(tb, err)
	}
}

func countOutboxRows(tb testing.TB, database db.IDatabase, where string, args ...any) int64 {
	tb.Helper()
	var count int64
	require.NoError(tb, database.QueryRow(context.Background(),
		fmt.Sprintf("SELECT COUNT(1) FROM event_outbox WHERE %s", where), args...).Scan(&count))
	return count
}

// TestSQLOutboxRepository_DeletePublished_KeysetCursorAndHeaderRetention 验证：
// 多批删除按主键游标推进（每批 SELECT 从上批最大 id 之后扫描）；不可删除的
// “表头”已发布记录（未过阈值）全程保留且不拖累扫描。
func TestSQLOutboxRepository_DeletePublished_KeysetCursorAndHeaderRetention(t *testing.T) {
	database := setupTestDB(t)
	eventStore := &MockEventStoreWithDB{}
	repo, err := NewSimpleSQLOutboxRepository(database, eventStore, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now()
	olderThan := now

	// 表头：id 最小、已发布但 published_at 在阈值之后 → 不可删除。
	headerPublishedAt := now.Add(time.Hour)
	insertOutboxRows(t, database, "event_outbox", "header", 1, outbox.OutboxStatusPublished, &headerPublishedAt, now.Add(-time.Hour))
	// 可删除：2500 条 published 且 published_at 远早于阈值 → 跨 3 个批次。
	deletablePublishedAt := now.Add(-24 * time.Hour)
	insertOutboxRows(t, database, "event_outbox", "bulk", 2500, outbox.OutboxStatusPublished, &deletablePublishedAt, now.Add(-time.Hour))

	captureDB := &captureQueryDB{IDatabase: database}
	repo.db = captureDB

	err = repo.DeletePublished(ctx, olderThan)
	require.NoError(t, err)

	// 全部可删除行被清走（阈值前 published 行数为 0），表头保留。
	assert.Equal(t, int64(0), countOutboxRows(t, database, "published_at < ?", olderThan))
	assert.Equal(t, int64(1), countOutboxRows(t, database, "status = ?", outbox.OutboxStatusPublished))

	// 游标推进断言：3 批 SELECT，第 n 批从第 n-1 批最大 id 之后扫描。
	selects := captureDB.selectCalls()
	require.Len(t, selects, 3)
	lastOfBatch := func(batch int) int64 {
		// 批次 n 的最大可删除 id = 1（表头）+ n*1000。
		return int64(1 + batch*1000)
	}
	for i, sel := range selects {
		assert.True(t, strings.Contains(sel.query, " AND id > ? "), fmt.Sprintf("select %d missing keyset cursor: %s", i, sel.query))
		require.Len(t, sel.args, 3)
		if i == 0 {
			assert.Equal(t, int64(0), sel.args[2])
		} else {
			assert.Equal(t, lastOfBatch(i), sel.args[2])
		}
	}
}

// TestSQLOutboxRepository_DeletePublished_ExactlyOneBatch 验证批边界：
// 恰好一个满批时再补一次空 SELECT 确认清空后正常退出。
func TestSQLOutboxRepository_DeletePublished_ExactlyOneBatch(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now()
	olderThan := now
	publishedAt := now.Add(-24 * time.Hour)
	insertOutboxRows(t, database, "event_outbox", "single", deletePublishedBatchSize, outbox.OutboxStatusPublished, &publishedAt, now.Add(-time.Hour))

	captureDB := &captureQueryDB{IDatabase: database}
	repo.db = captureDB

	require.NoError(t, repo.DeletePublished(ctx, olderThan))
	assert.Equal(t, int64(0), countOutboxRows(t, database, "1=1"))

	// 满批（1000）+ 一次空确认 SELECT = 2 批查询。
	selects := captureDB.selectCalls()
	require.Len(t, selects, 2)
	require.Len(t, selects[1].args, 3)
	assert.Equal(t, int64(deletePublishedBatchSize), selects[1].args[2])
}

// TestSQLOutboxRepository_DeletePublished_ZeroRowsAffectedGuard 防住
// RowsAffected 恒 0 的异常驱动面：连续多批“选中即删 0 行”时报错退出，
// 不以 10ms 节奏空转到 ctx 取消。
func TestSQLOutboxRepository_DeletePublished_ZeroRowsAffectedGuard(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now()
	olderThan := now
	publishedAt := now.Add(-24 * time.Hour)
	// 3 个满批 + 尾批，使零进度连续达到阈值。
	insertOutboxRows(t, database, "event_outbox", "zero", deletePublishedBatchSize*3+13, outbox.OutboxStatusPublished, &publishedAt, now.Add(-time.Hour))

	repo.db = zeroAffectedNoExecDB{IDatabase: database}

	done := make(chan error, 1)
	go func() {
		done <- repo.DeletePublished(ctx, olderThan)
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		require.True(t, errors.Is(err, errors.Database))
	case <-time.After(5 * time.Second):
		t.Fatal("DeletePublished did not terminate under zero-rows-affected driver")
	}
	// 异常驱动下没有行被真实删除（DELETE 被拦截），数据未静默丢失。
	assert.Equal(t, int64(deletePublishedBatchSize*3+13), countOutboxRows(t, database, "1=1"))
}

// TestDeletePublishedQueries_DialectMatrix 校验三方言生成的 SELECT/DELETE SQL：
// 引用符正确、keyset 游标谓词在位、占位符与参数个数一致（无需真实 DB 实例）。
func TestDeletePublishedQueries_DialectMatrix(t *testing.T) {
	cases := []struct {
		name     string
		driver   string
		expected string
	}{
		{"mysql", "mysql", "`event_outbox`"},
		{"postgres", "postgres", `"event_outbox"`},
		{"sqlite", "sqlite", `"event_outbox"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quoted := rtdialect.New(tc.driver).QuoteIdentifier("event_outbox")
			assert.Equal(t, tc.expected, quoted)

			selectQuery := buildSelectPublishedIDsQuery(quoted, deletePublishedBatchSize)
			assert.Equal(t,
				fmt.Sprintf("SELECT id FROM %s WHERE status = ? AND published_at < ? AND id > ? ORDER BY id LIMIT %d", quoted, deletePublishedBatchSize),
				selectQuery)
			assert.Equal(t, 3, strings.Count(selectQuery, "?"))

			deleteQuery := buildDeletePublishedByIDsQuery(quoted, 3)
			assert.Equal(t,
				fmt.Sprintf("DELETE FROM %s WHERE status = ? AND published_at < ? AND id IN (?,?,?)", quoted),
				deleteQuery)
			assert.Equal(t, 5, strings.Count(deleteQuery, "?"))
		})
	}
}

// TestSQLOutboxRepository_DeletePublished_CanceledIsNotTimeout 验证主动取消
// 不再被包装为 Timeout 错误码（ee3afcb）。
func TestSQLOutboxRepository_DeletePublished_CanceledIsNotTimeout(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now()
	olderThan := now
	publishedAt := now.Add(-24 * time.Hour)
	insertOutboxRows(t, database, "event_outbox", "canceled", deletePublishedBatchSize*2, outbox.OutboxStatusPublished, &publishedAt, now.Add(-time.Hour))

	canceledCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	err = repo.DeletePublished(canceledCtx, olderThan)
	require.Error(t, err)
	// Canceled 不应归类为 Timeout（不触发按超时重试/告警的调用方语义）。
	assert.False(t, errors.Is(err, errors.Timeout))
	assert.True(t, errors.Is(err, context.Canceled))
}

// TestSQLOutboxRepository_DeletePublished_DeadlineExceededIsTimeout 端到端用例：
// 超时仍归类为 Timeout。预算取 1ms——2×batch 行数必然产生一次满批，满批后
// 无条件休眠 deletePublishedSleepInterval（10ms），故删除必然在 1ms 预算内
// 无法完成，超时必然发生；具体落在 SELECT / DELETE / 休眠哪一阶段不做断言
// （各阶段均归 Timeout，阶段级确定性由下方两个阻塞 DB 用例分别守护）。
//
// 注意不要把预算放大到接近实际耗时（如 200ms）：那样小数据量会在预算内删完、
// DeletePublished 返回 nil，测试退化为时钟竞争（曾因此确定性失败）。
func TestSQLOutboxRepository_DeletePublished_DeadlineExceededIsTimeout(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)

	ctx := context.Background()
	now := time.Now()
	olderThan := now
	publishedAt := now.Add(-24 * time.Hour)
	insertOutboxRows(t, database, "event_outbox", "deadline", deletePublishedBatchSize*2, outbox.OutboxStatusPublished, &publishedAt, now.Add(-time.Hour))

	deadlineCtx, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()

	err = repo.DeletePublished(deadlineCtx, olderThan)
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.Timeout))
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}

// selectBlockingDB 模拟 SELECT 阶段阻塞直到 ctx 取消的慢 DB：使“SELECT 阶段
// 超时”成为确定性行为，不再依赖时钟竞争。
type selectBlockingDB struct {
	db.IDatabase
}

func (d selectBlockingDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if strings.Contains(query, "SELECT id FROM") {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.IDatabase.Query(ctx, query, args...)
}

// TestSQLOutboxRepository_DeletePublished_SelectPhaseDeadlineIsTimeout 验证
// SELECT 阶段的 DeadlineExceeded 同样归类为 Timeout（分类口径统一修复前
// 该阶段被包装为 Database，与批间休眠阶段的 Timeout 漂移）。
func TestSQLOutboxRepository_DeletePublished_SelectPhaseDeadlineIsTimeout(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)

	repo.db = selectBlockingDB{IDatabase: database}

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err = repo.DeletePublished(deadlineCtx, time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.Timeout))
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}

// deletePhaseBlockingDB SELECT 返回一批可删除 id，DELETE 阻塞直到 ctx 取消：
// 使“DELETE 阶段超时”成为确定性行为。
type deletePhaseBlockingDB struct {
	db.IDatabase
	queried bool
}

func (d *deletePhaseBlockingDB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if strings.Contains(query, "SELECT id FROM") && !d.queried {
		d.queried = true
		return &singleIDRows{}, nil
	}
	return d.IDatabase.Query(ctx, query, args...)
}

func (d *deletePhaseBlockingDB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(query)), "DELETE") {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.IDatabase.Exec(ctx, query, args...)
}

// singleIDRows 只产出一行主键的行集 stub（供 deletePhaseBlockingDB 使用）。
//
// 接收者必须是指针：Next/Scan 共享 consumed 状态。用值接收者时 Scan 里的
// `r.consumed = true` 只改副本，Next 恒为 true → selectPublishedIDs 无限
// append 主键直到进程内存耗尽（曾实测 20s 后被 OOM kill）。
type singleIDRows struct {
	consumed bool
}

func (r *singleIDRows) Next() bool { return !r.consumed }
func (r *singleIDRows) Scan(dest ...any) error {
	if r.consumed {
		return stdErrors.New("no more rows")
	}
	*(dest[0].(*int64)) = 1
	r.consumed = true
	return nil
}
func (r *singleIDRows) Close() error                            { return nil }
func (r *singleIDRows) Err() error                              { return nil }
func (r *singleIDRows) Columns() ([]string, error)              { return []string{"id"}, nil }
func (r *singleIDRows) ColumnTypes() ([]*sql.ColumnType, error) { return nil, nil }

// TestSQLOutboxRepository_DeletePublished_DeletePhaseDeadlineIsTimeout 验证
// DELETE 阶段的 DeadlineExceeded 同样归类为 Timeout（同 SELECT 阶段口径）。
func TestSQLOutboxRepository_DeletePublished_DeletePhaseDeadlineIsTimeout(t *testing.T) {
	database := setupTestDB(t)
	repo, err := NewSimpleSQLOutboxRepository(database, &MockEventStoreWithDB{}, logging.NewNoopLogger())
	require.NoError(t, err)

	repo.db = &deletePhaseBlockingDB{IDatabase: database}

	deadlineCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err = repo.DeletePublished(deadlineCtx, time.Now())
	require.Error(t, err)
	assert.True(t, errors.Is(err, errors.Timeout))
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}
