package sqlstore

import (
	"context"
	cryptorand "crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	coredialect "gochen/db/dialect"
	mrand "math/rand"
	"strings"
	"sync"
	"time"

	"gochen-runtime/db/dialect"
	"gochen-runtime/db/schema"
	"gochen/db"
	"gochen/errors"
)

var (
	globalPositionRetryRandMu sync.Mutex
	globalPositionRetryRand   = mrand.New(mrand.NewSource(globalPositionRetrySeed()))
	// test hook: allow deterministic jitter assertions without sleeping.
	globalPositionRetryFloat64 = func() float64 {
		globalPositionRetryRandMu.Lock()
		defer globalPositionRetryRandMu.Unlock()
		return globalPositionRetryRand.Float64()
	}
)

const maxGlobalPositionRetries = 10

const streamHorizonCacheTTL = 50 * time.Millisecond

const globalPositionProbeCacheTTL = 500 * time.Millisecond

const globalPositionAllocatorTable = "event_store_positions"

func (s *SQLEventStore[ID]) supportsGlobalPositionWithError(ctx context.Context, database db.IDatabase) (bool, error) {
	if s == nil || database == nil {
		return false, nil
	}
	if s.globalPositionSupport.Load() == 1 {
		return true, nil
	}
	supported, err := s.probeGlobalPositionSupport(ctx, database)
	if err != nil {
		return false, err
	}
	if supported {
		s.globalPositionSupport.Store(1)
	}
	return supported, nil
}

func (s *SQLEventStore[ID]) supportsGlobalPositionForStream(ctx context.Context, database db.IDatabase) (bool, error) {
	return s.supportsGlobalPositionWithNegativeCache(ctx, database)
}

func (s *SQLEventStore[ID]) supportsGlobalPositionWithNegativeCache(ctx context.Context, database db.IDatabase) (bool, error) {
	if s == nil || database == nil {
		return false, nil
	}
	if s.globalPositionSupport.Load() == 1 {
		return true, nil
	}
	if supported, ok := s.cachedGlobalPositionProbe(); ok {
		return supported, nil
	}
	supported, err := s.supportsGlobalPositionWithError(ctx, database)
	if err != nil {
		return false, err
	}
	s.cacheGlobalPositionProbe(supported)
	return supported, nil
}

func (s *SQLEventStore[ID]) probeGlobalPositionSupport(ctx context.Context, database db.IDatabase) (bool, error) {
	rows, err := database.Query(ctx, fmt.Sprintf("SELECT * FROM %s WHERE 1 = 0", s.tableName))
	if err != nil {
		return false, err
	}
	columns, err := rows.Columns()
	if err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, column := range columns {
		if strings.EqualFold(column, "global_position") {
			if err := s.validateGlobalPositionUniqueConstraint(ctx, database); err != nil {
				return false, err
			}
			if err := s.validateGlobalPositionAllocator(ctx, database); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func (s *SQLEventStore[ID]) cachedGlobalPositionProbe() (bool, bool) {
	s.globalPositionMu.Lock()
	defer s.globalPositionMu.Unlock()
	if time.Now().Before(s.globalPositionProbe.expiresAt) {
		return s.globalPositionProbe.supported, true
	}
	return false, false
}

func (s *SQLEventStore[ID]) cacheGlobalPositionProbe(supported bool) {
	s.globalPositionMu.Lock()
	s.globalPositionProbe = globalPositionProbeCache{
		expiresAt: time.Now().Add(globalPositionProbeCacheTTL),
		supported: supported,
	}
	s.globalPositionMu.Unlock()
}

func (s *SQLEventStore[ID]) validateGlobalPositionUniqueConstraint(ctx context.Context, database db.IDatabase) error {
	d := dialect.FromDatabase(database)
	var (
		ok  bool
		err error
	)
	switch d.Name() {
	case coredialect.NameSQLite:
		ok, err = s.hasSQLiteGlobalPositionUniqueIndex(ctx, database)
	case coredialect.NameMySQL:
		ok, err = s.hasMySQLGlobalPositionUniqueIndex(ctx, database)
	case coredialect.NamePostgres:
		ok, err = s.hasPostgresGlobalPositionUniqueIndex(ctx, database)
	default:
		return errors.NewCode(errors.Unsupported, "global_position requires unique index validation for current database dialect").
			WithContext("table", s.tableName).
			WithContext("dialect", string(d.Name()))
	}
	if err != nil {
		return errors.Wrap(err, errors.Database, "validate global_position unique index failed").
			WithContext("table", s.tableName)
	}
	if !ok {
		return errors.NewCode(errors.InvalidInput, "global_position column requires a unique index or constraint").
			WithContext("table", s.tableName).
			WithContext("column", "global_position")
	}
	return nil
}

func (s *SQLEventStore[ID]) hasSQLiteGlobalPositionUniqueIndex(ctx context.Context, database db.IDatabase) (bool, error) {
	schemaName, tableName := schema.ParseTableName(s.tableName)
	rows, err := database.Query(ctx, fmt.Sprintf("PRAGMA %sindex_list(%s)",
		sqlitePragmaSchemaPrefix(schemaName),
		dialect.New("sqlite").QuoteIdentifier(tableName)))
	if err != nil {
		return false, err
	}
	var indexNames []string
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return false, err
		}
		_, _ = seq, origin
		if unique == 0 || partial != 0 || name == "" {
			continue
		}
		indexNames = append(indexNames, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, name := range indexNames {
		ok, err := sqliteIndexOnlyGlobalPosition(ctx, database, schemaName, name)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func sqlitePragmaSchemaPrefix(schemaName string) string {
	if schemaName == "" {
		return ""
	}
	return dialect.New("sqlite").QuoteIdentifier(schemaName) + "."
}

func sqliteIndexOnlyGlobalPosition(ctx context.Context, database db.IDatabase, schemaName, indexName string) (bool, error) {
	rows, err := database.Query(ctx, fmt.Sprintf("PRAGMA %sindex_info(%s)",
		sqlitePragmaSchemaPrefix(schemaName),
		dialect.New("sqlite").QuoteIdentifier(indexName)))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	matched := false
	for rows.Next() {
		var (
			seqno int
			cid   int
			name  sql.NullString
		)
		if err := rows.Scan(&seqno, &cid, &name); err != nil {
			return false, err
		}
		_, _ = seqno, cid
		count++
		if !name.Valid || strings.TrimSpace(name.String) == "" {
			return false, nil
		}
		if !strings.EqualFold(name.String, "global_position") {
			return false, nil
		}
		matched = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return count == 1 && matched, nil
}

func (s *SQLEventStore[ID]) hasMySQLGlobalPositionUniqueIndex(ctx context.Context, database db.IDatabase) (bool, error) {
	schemaName, tableName := schema.ParseTableName(s.tableName)
	tableSchemaPredicate := "table_schema = DATABASE()"
	args := []any{tableName}
	if schemaName != "" {
		tableSchemaPredicate = "table_schema = ?"
		args = append([]any{schemaName}, args...)
	}
	row := database.QueryRow(ctx, `
SELECT COUNT(*)
FROM (
	SELECT index_name
	FROM information_schema.statistics
	WHERE `+tableSchemaPredicate+`
	  AND table_name = ?
	  AND non_unique = 0
	GROUP BY index_name
	HAVING COUNT(*) = 1 AND MAX(column_name) = 'global_position'
) gp_unique`, args...)
	var count int
	if err := row.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *SQLEventStore[ID]) hasPostgresGlobalPositionUniqueIndex(ctx context.Context, database db.IDatabase) (bool, error) {
	schemaName, tableName := schema.ParseTableName(s.tableName)
	schemaPredicate := "n.nspname = ANY(current_schemas(false))"
	args := []any{tableName}
	if schemaName != "" {
		schemaPredicate = "n.nspname = ?"
		args = append(args, schemaName)
	}
	row := database.QueryRow(ctx, `
SELECT COUNT(*)
FROM pg_index i
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE t.relname = ?
  AND `+schemaPredicate+`
  AND i.indisunique
  AND i.indnatts = 1
  AND (
	SELECT a.attname
	FROM pg_attribute a
	WHERE a.attrelid = t.oid
	  AND a.attnum = i.indkey[0]
  ) = 'global_position'`, args...)
	var count int
	if err := row.Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *SQLEventStore[ID]) validateGlobalPositionAllocator(ctx context.Context, database db.IDatabase) error {
	d := dialect.FromDatabase(database)
	allocatorTable := d.QuoteIdentifier(s.globalPositionAllocatorTableName())
	row := database.QueryRow(ctx,
		fmt.Sprintf("SELECT COUNT(*), MIN(next_position) FROM %s WHERE store_name = ?", allocatorTable),
		s.globalPositionStoreName(),
	)
	var count int64
	var nextPosition sql.NullInt64
	if err := row.Scan(&count, &nextPosition); err != nil {
		return errors.NewCodeWithCause(errors.InvalidInput, "global_position requires event_store_positions allocator table", err).
			WithContext("table", s.globalPositionAllocatorTableName()).
			WithContext("store_name", s.globalPositionStoreName())
	}
	if count != 1 {
		return errors.NewCode(errors.InvalidInput, "event_store_positions requires exactly one allocator row for event store").
			WithContext("table", s.globalPositionAllocatorTableName()).
			WithContext("store_name", s.globalPositionStoreName()).
			WithContext("row_count", count)
	}
	if !nextPosition.Valid || nextPosition.Int64 <= 0 {
		return errors.NewCode(errors.InvalidInput, "event_store_positions next_position must be positive").
			WithContext("table", s.globalPositionAllocatorTableName()).
			WithContext("store_name", s.globalPositionStoreName()).
			WithContext("next_position", nextPosition.Int64)
	}
	return nil
}

func (s *SQLEventStore[ID]) reserveGlobalPositions(ctx context.Context, database db.IDatabase, count int) (int64, error) {
	if count <= 0 {
		return 0, errors.NewCode(errors.InvalidInput, "global position reservation count must be positive").
			WithContext("count", count)
	}
	d := dialect.FromDatabase(database)
	switch d.Name() {
	case coredialect.NameSQLite, coredialect.NamePostgres:
		return s.reserveGlobalPositionsReturning(ctx, database, d, count)
	case coredialect.NameMySQL:
		return s.reserveGlobalPositionsWithRowLock(ctx, database, d, count)
	default:
		return 0, errors.NewCode(errors.Unsupported, "global_position allocator requires supported database dialect").
			WithContext("dialect", string(d.Name()))
	}
}

func (s *SQLEventStore[ID]) reserveGlobalPositionsReturning(ctx context.Context, database db.IDatabase, d coredialect.IDialect, count int) (int64, error) {
	row := database.QueryRow(ctx,
		fmt.Sprintf("UPDATE %s SET next_position = next_position + ? WHERE store_name = ? RETURNING next_position",
			d.QuoteIdentifier(s.globalPositionAllocatorTableName())),
		count,
		s.globalPositionStoreName(),
	)
	var nextPosition int64
	if err := row.Scan(&nextPosition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, missingGlobalPositionAllocatorRowError(s.globalPositionAllocatorTableName(), s.globalPositionStoreName())
		}
		return 0, err
	}
	startPosition := nextPosition - int64(count)
	if startPosition <= 0 {
		return 0, errors.NewCode(errors.InvalidInput, "event_store_positions allocated invalid global position").
			WithContext("table", s.globalPositionAllocatorTableName()).
			WithContext("store_name", s.globalPositionStoreName()).
			WithContext("start_position", startPosition)
	}
	return startPosition, nil
}

func (s *SQLEventStore[ID]) reserveGlobalPositionsWithRowLock(ctx context.Context, database db.IDatabase, d coredialect.IDialect, count int) (int64, error) {
	allocatorTable := d.QuoteIdentifier(s.globalPositionAllocatorTableName())
	row := database.QueryRow(ctx,
		fmt.Sprintf("SELECT next_position FROM %s WHERE store_name = ? FOR UPDATE", allocatorTable),
		s.globalPositionStoreName(),
	)
	var startPosition int64
	if err := row.Scan(&startPosition); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, missingGlobalPositionAllocatorRowError(s.globalPositionAllocatorTableName(), s.globalPositionStoreName())
		}
		return 0, err
	}
	if startPosition <= 0 {
		return 0, errors.NewCode(errors.InvalidInput, "event_store_positions next_position must be positive").
			WithContext("table", s.globalPositionAllocatorTableName()).
			WithContext("store_name", s.globalPositionStoreName()).
			WithContext("next_position", startPosition)
	}
	result, err := database.Exec(ctx,
		fmt.Sprintf("UPDATE %s SET next_position = ? WHERE store_name = ?", allocatorTable),
		startPosition+int64(count),
		s.globalPositionStoreName(),
	)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, errors.NewCodeWithCause(errors.Database, "reserve global event positions rows affected failed", err).
			WithContext("table", s.globalPositionAllocatorTableName()).
			WithContext("store_name", s.globalPositionStoreName())
	}
	if affected != 1 {
		return 0, errors.NewCode(errors.Concurrency, "reserve global event positions affected unexpected rows").
			WithContext("table", s.globalPositionAllocatorTableName()).
			WithContext("store_name", s.globalPositionStoreName()).
			WithContext("rows_affected", affected)
	}
	return startPosition, nil
}

func missingGlobalPositionAllocatorRowError(tableName string, storeName string) error {
	return errors.NewCode(errors.InvalidInput, "event_store_positions allocator row is required").
		WithContext("table", tableName).
		WithContext("store_name", storeName)
}

func (s *SQLEventStore[ID]) globalPositionAllocatorTableName() string {
	schemaName, _ := schema.ParseTableName(s.tableName)
	if schemaName == "" {
		return globalPositionAllocatorTable
	}
	return schemaName + "." + globalPositionAllocatorTable
}

func (s *SQLEventStore[ID]) globalPositionStoreName() string {
	return s.tableName
}

func isGlobalPositionUniqueError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "global_position") && isDuplicateKeyError(err)
}

func globalPositionRetrySeed() int64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err == nil {
		return int64(binary.LittleEndian.Uint64(b[:]))
	}
	return time.Now().UnixNano()
}

func globalPositionRetryDelay(attempt int) time.Duration {
	base := time.Millisecond << min(attempt, 5)
	jittered := time.Duration(float64(base) * (0.5 + globalPositionRetryFloat64()))
	if jittered <= 0 {
		return time.Millisecond
	}
	return jittered
}

func waitGlobalPositionRetry(ctx context.Context, attempt int) error {
	delay := globalPositionRetryDelay(attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// streamHorizon 计算可安全读取的全局位置上界（high-water-mark）。
//
// 返回从 1 开始的首个连续区段末位置，
// 使订阅游标不会越过仍可能被在途事务补齐的空洞，避免晚提交事件被永久跳过。
//
// 第一个返回值为 false 表示当前没有任何非 NULL 全局位置（空表或仅有 NULL 历史行），
// 调用方此时不应施加位置上界过滤。
//
// 性能：先用 MIN/MAX 识别空表与边界，再按 global_position 索引顺序查首个 gap。
// cache 会在本 store 写入后失效；TTL 主要覆盖无事件轮询和跨进程写入兜底。
func (s *SQLEventStore[ID]) streamHorizon(ctx context.Context, database db.IDatabase) (bool, int64, error) {
	if cached, ok := s.cachedStreamHorizon(); ok {
		return cached.has, cached.horizon, nil
	}
	previous := s.lastStreamHorizon()

	var minPos, maxPos sql.NullInt64
	row := database.QueryRow(ctx, fmt.Sprintf(
		"SELECT MIN(global_position), MAX(global_position) FROM %s WHERE global_position IS NOT NULL",
		s.tableName))
	if err := row.Scan(&minPos, &maxPos); err != nil {
		return false, 0, err
	}
	if !minPos.Valid || !maxPos.Valid {
		s.cacheStreamHorizon(false, 0)
		return false, 0, nil
	}
	if minPos.Int64 > 1 {
		s.cacheStreamHorizon(true, 0)
		return true, 0, nil
	}
	// 存在空洞：取首个连续区段末位置（其后继位置缺失的最小位置）。
	var runEnd sql.NullInt64
	lowerBoundSQL := ""
	gapArgs := []any{maxPos.Int64}
	if previous.has && previous.horizon > 0 && previous.horizon <= maxPos.Int64 {
		lowerBoundSQL = "AND t1.global_position >= ? "
		gapArgs = []any{previous.horizon, maxPos.Int64}
	}
	gapRow := database.QueryRow(ctx, fmt.Sprintf(
		"SELECT t1.global_position FROM %s t1 WHERE t1.global_position IS NOT NULL "+
			lowerBoundSQL+
			"AND t1.global_position < ? "+
			"AND NOT EXISTS (SELECT 1 FROM %s t2 WHERE t2.global_position = t1.global_position + 1) "+
			"ORDER BY t1.global_position LIMIT 1",
		s.tableName, s.tableName), gapArgs...)
	if err := gapRow.Scan(&runEnd); err != nil {
		if err == sql.ErrNoRows {
			s.cacheStreamHorizon(true, maxPos.Int64)
			return true, maxPos.Int64, nil
		}
		return false, 0, err
	}
	if !runEnd.Valid {
		s.cacheStreamHorizon(true, maxPos.Int64)
		return true, maxPos.Int64, nil
	}
	s.cacheStreamHorizon(true, runEnd.Int64)
	return true, runEnd.Int64, nil
}

func (s *SQLEventStore[ID]) cachedStreamHorizon() (streamHorizonCache, bool) {
	if s == nil {
		return streamHorizonCache{}, false
	}
	s.streamHorizonMu.Lock()
	defer s.streamHorizonMu.Unlock()
	if s.streamHorizonCache.expiresAt.IsZero() || time.Now().After(s.streamHorizonCache.expiresAt) {
		return streamHorizonCache{}, false
	}
	return s.streamHorizonCache, true
}

func (s *SQLEventStore[ID]) lastStreamHorizon() streamHorizonCache {
	if s == nil {
		return streamHorizonCache{}
	}
	s.streamHorizonMu.Lock()
	defer s.streamHorizonMu.Unlock()
	return s.streamHorizonCache
}

func (s *SQLEventStore[ID]) cacheStreamHorizon(has bool, horizon int64) {
	if s == nil || streamHorizonCacheTTL <= 0 {
		return
	}
	s.streamHorizonMu.Lock()
	defer s.streamHorizonMu.Unlock()
	s.streamHorizonCache = streamHorizonCache{
		expiresAt: time.Now().Add(streamHorizonCacheTTL),
		has:       has,
		horizon:   horizon,
	}
}

func (s *SQLEventStore[ID]) invalidateStreamHorizonCache() {
	if s == nil {
		return
	}
	s.streamHorizonMu.Lock()
	defer s.streamHorizonMu.Unlock()
	s.streamHorizonCache.expiresAt = time.Time{}
}
