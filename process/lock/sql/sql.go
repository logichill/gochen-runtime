package sql

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"gochen-runtime/db/sql/safeident"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
	"gochen/observe/logging"
	"gochen/process/lock"
	"strings"
	"sync"
	"time"
)

// SQLLockProviderConfig SQL 分布式锁配置。
type SQLLockProviderConfig struct {
	// TableName 锁表名（默认：distributed_locks）
	TableName string

	// Owner 当前实例标识（用于安全释放）。
	Owner string

	// TTL 锁过期时间（默认：30s）。
	TTL time.Duration

	// PollInterval 未获取到锁时的轮询等待时间（默认：50ms）。
	PollInterval time.Duration

	// RenewInterval 持锁期间续租间隔（默认 TTL/2；小于 0 时关闭续租，主要用于兼容测试）。
	RenewInterval time.Duration

	Logger logging.ILogger
}

// SQLLockProvider 基于共享数据库表的分布式锁实现。
//
// 表结构（自动创建）：
// - lock_key TEXT PRIMARY KEY
// - owner TEXT NOT NULL
// - lease_token TEXT NOT NULL
// - expires_at_ms INTEGER NOT NULL
type SQLLockProvider struct {
	db            db.IDatabase
	tableName     string
	owner         string
	ttl           time.Duration
	pollInterval  time.Duration
	renewInterval time.Duration
	logger        logging.ILogger

	ensureMu  sync.Mutex
	ensured   bool
	ensureErr error
}

// NewSQLLockProvider 创建SQLLock提供者。
func NewSQLLockProvider(database db.IDatabase, cfg *SQLLockProviderConfig) (*SQLLockProvider, error) {
	if database == nil {
		return nil, errors.NewCode(errors.InvalidInput, "database cannot be nil")
	}
	if cfg == nil {
		cfg = &SQLLockProviderConfig{}
	} else {
		cfgCopy := *cfg
		cfg = &cfgCopy
	}
	if cfg.TableName == "" {
		cfg.TableName = "distributed_locks"
	}
	if !safeident.IsSafeIdentifier(cfg.TableName) || strings.Contains(cfg.TableName, ".") {
		return nil, errors.NewCode(errors.InvalidInput, fmt.Sprintf("invalid table name: %s", cfg.TableName))
	}
	if cfg.Owner == "" {
		return nil, errors.NewCode(errors.InvalidInput, "owner cannot be empty")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 30 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 50 * time.Millisecond
	}
	if cfg.RenewInterval == 0 {
		cfg.RenewInterval = cfg.TTL / 2
	}
	if cfg.RenewInterval > 0 && cfg.RenewInterval >= cfg.TTL {
		cfg.RenewInterval = cfg.TTL / 2
	}
	if cfg.Logger == nil {
		cfg.Logger = logging.ComponentLogger("process.lock.sql").WithField("table", cfg.TableName)
	}

	return &SQLLockProvider{
		db:            database,
		tableName:     cfg.TableName,
		owner:         cfg.Owner,
		ttl:           cfg.TTL,
		pollInterval:  cfg.PollInterval,
		renewInterval: cfg.RenewInterval,
		logger:        cfg.Logger,
	}, nil
}

// ensureTable 确保表。
func (p *SQLLockProvider) ensureTable(ctx context.Context) error {
	p.ensureMu.Lock()
	defer p.ensureMu.Unlock()
	if p.ensured {
		return p.ensureErr
	}

	func() {
		ddl := p.lockTableDDL()
		_, err := p.db.Exec(ctx, ddl)
		if err != nil {
			p.ensureErr = err
			return
		}
		hasLeaseToken, err := p.hasLeaseTokenColumn(ctx)
		if err != nil {
			p.ensureErr = err
			return
		}
		if hasLeaseToken {
			p.ensureErr = nil
			return
		}
		_, err = p.db.Exec(ctx, p.addLeaseTokenColumnDDL())
		if err != nil {
			hasLeaseToken, checkErr := p.hasLeaseTokenColumn(ctx)
			if checkErr != nil {
				p.ensureErr = checkErr
				return
			}
			if !hasLeaseToken {
				p.ensureErr = err
				return
			}
		}
		p.ensureErr = nil
	}()
	p.ensured = p.ensureErr == nil
	return p.ensureErr
}

func (p *SQLLockProvider) lockTableDDL() string {
	lockKeyType, ownerType, leaseTokenType := p.lockStringColumnTypes()
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
lock_key %s PRIMARY KEY,
owner %s NOT NULL,
lease_token %s NOT NULL DEFAULT '',
expires_at_ms BIGINT NOT NULL
)`, p.quotedTableName(), lockKeyType, ownerType, leaseTokenType)
}

func (p *SQLLockProvider) addLeaseTokenColumnDDL() string {
	_, _, leaseTokenType := p.lockStringColumnTypes()
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN lease_token %s NOT NULL DEFAULT ''", p.quotedTableName(), leaseTokenType)
}

func (p *SQLLockProvider) lockStringColumnTypes() (string, string, string) {
	switch dialect.FromDatabase(p.db).Name() {
	case dialect.NameMySQL:
		return "VARCHAR(255)", "VARCHAR(255)", "VARCHAR(64)"
	case dialect.NamePostgres:
		return "TEXT", "TEXT", "TEXT"
	default:
		return "TEXT", "TEXT", "TEXT"
	}
}

func (p *SQLLockProvider) quotedTableName() string {
	return dialect.FromDatabase(p.db).QuoteIdentifier(p.tableName)
}

func (p *SQLLockProvider) hasLeaseTokenColumn(ctx context.Context) (bool, error) {
	switch dialect.FromDatabase(p.db).Name() {
	case dialect.NameMySQL:
		return p.hasColumnByInformationSchema(ctx)
	case dialect.NamePostgres:
		return p.hasColumnByCatalog(ctx)
	default:
		return p.hasColumnByZeroRowProbe(ctx)
	}
}

func (p *SQLLockProvider) hasColumnByInformationSchema(ctx context.Context) (bool, error) {
	rows, err := p.db.Query(ctx, "SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?",
		p.tableName, "lease_token")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if rows.Next() {
		return true, nil
	}
	return false, rows.Err()
}

func (p *SQLLockProvider) hasColumnByCatalog(ctx context.Context) (bool, error) {
	rows, err := p.db.Query(ctx, `
SELECT 1
FROM pg_catalog.pg_attribute a
JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relname = ?
  AND n.nspname = COALESCE(current_schema(), 'public')
  AND a.attname = ?
  AND NOT a.attisdropped`,
		p.tableName, "lease_token")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if rows.Next() {
		return true, nil
	}
	return false, rows.Err()
}

func (p *SQLLockProvider) hasColumnByZeroRowProbe(ctx context.Context) (bool, error) {
	rows, err := p.db.Query(ctx, fmt.Sprintf("SELECT * FROM %s WHERE 1 = 0", p.quotedTableName()))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return false, err
	}
	for _, column := range columns {
		if strings.EqualFold(column, "lease_token") {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (p *SQLLockProvider) Acquire(ctx context.Context, key string) (func(), error) {
	lease, err := p.AcquireLease(ctx, key)
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

func (p *SQLLockProvider) AcquireLease(ctx context.Context, key string) (lock.ILockLease, error) {
	if key == "" {
		return nil, errors.NewCode(errors.InvalidInput, "lock key cannot be empty")
	}
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if err := p.ensureTable(ctx); err != nil {
		if classified := classifyLockError(err, "ensure lock table failed", key); classified != nil {
			return nil, classified
		}
		return nil, errors.NewCodeWithCause(errors.Database, "ensure lock table failed", err)
	}

	start := time.Now()
	// 复用 poll timer，避免在热重试循环中用 time.After 产生逐次迭代的分配。
	pollTimer := time.NewTimer(0)
	if !pollTimer.Stop() {
		select {
		case <-pollTimer.C:
		default:
		}
	}
	defer pollTimer.Stop()

	leaseToken, tokenErr := newLeaseToken()
	if tokenErr != nil {
		return nil, tokenErr
	}
	quotedTable := p.quotedTableName()
	insertSQL := fmt.Sprintf("INSERT INTO %s (lock_key, owner, lease_token, expires_at_ms) VALUES (?, ?, ?, ?)", quotedTable)
	updateSQL := fmt.Sprintf("UPDATE %s SET owner = ?, lease_token = ?, expires_at_ms = ? WHERE lock_key = ? AND expires_at_ms <= ?", quotedTable)

	for {
		now := time.Now().UnixMilli()
		expires := now + p.ttl.Milliseconds()

		_, insertErr := p.db.Exec(ctx,
			insertSQL,
			key, p.owner, leaseToken, expires,
		)
		if insertErr == nil {
			return p.newLease(key, leaseToken), nil
		}
		if !isDuplicateLockInsertError(insertErr) {
			if classified := classifyLockError(insertErr, "lock acquire insert failed", key); classified != nil {
				return nil, classified
			}
			return nil, errors.NewCodeWithCause(errors.Database, "lock acquire insert failed", insertErr).
				WithContext("key", key)
		}

		// 尝试抢占过期锁
		res, uerr := p.db.Exec(ctx,
			updateSQL,
			p.owner, leaseToken, expires, key, now,
		)
		if uerr != nil {
			if classified := classifyLockError(uerr, "lock acquire update failed", key); classified != nil {
				return nil, classified
			}
			return nil, errors.NewCodeWithCause(errors.Database, "lock acquire update failed", uerr).
				WithContext("key", key)
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			p.logger.Warn(ctx, "lock acquire update rows affected failed", logging.Error(aerr), logging.String("key", key))
			return nil, errors.NewCodeWithCause(errors.Database, "lock acquire update rows affected failed", aerr).
				WithContext("key", key)
		} else if affected == 1 {
			return p.newLease(key, leaseToken), nil
		}

		if err := classifyAcquireContextError(ctx, key, start); err != nil {
			return nil, err
		}

		// 等待下一次尝试
		if !pollTimer.Stop() {
			select {
			case <-pollTimer.C:
			default:
			}
		}
		pollTimer.Reset(p.pollInterval)
		select {
		case <-pollTimer.C:
		case <-ctx.Done():
			// 与其它阶段共用同一分类器：等待期间超时必须带 Timeout 码、保留
			// DeadlineExceeded cause，并补齐 key/ms 上下文，便于定位是哪把锁等超时。
			return nil, classifyAcquireContextError(ctx, key, start)
		}
	}
}

func classifyAcquireContextError(ctx context.Context, key string, start time.Time) error {
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.NewCodeWithCause(errors.Timeout, "lock acquire timeout", ctx.Err()).
				WithContext("key", key).
				WithContext("ms", time.Since(start).Milliseconds())
		}
		return ctx.Err()
	default:
		return nil
	}
}

func classifyLockError(err error, message string, key string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.NewCodeWithCause(errors.Timeout, message, err).WithContext("key", key)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func newLeaseToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.NewCodeWithCause(errors.Internal, "generate lock lease token failed", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func isDuplicateLockInsertError(err error) bool {
	return db.IsUniqueViolation(err)
}

func (p *SQLLockProvider) newLease(key string, leaseToken string) *lock.Lease {
	safeLost := lock.NewSafeLostChan()
	stopRenew := p.startRenewal(key, leaseToken, safeLost)
	return lock.NewLease(p.releaseFunc(key, leaseToken, stopRenew), safeLost.Channel())
}

// maxRenewAttempts 是单个续租周期内对瞬时数据库错误的最大尝试次数。
// owner/lease_token 失配（Conflict）表示锁确实丢失，不重试，立即报告 lease lost；
// 仅对数据库瞬时错误（连接抖动、临时不可用）重试，避免误判 lease lost。
const maxRenewAttempts = 3

func (p *SQLLockProvider) startRenewal(key string, leaseToken string, lost *lock.SafeLostChan) func() {
	if p.renewInterval < 0 {
		return func() {
			lost.Close(nil)
		}
	}
	interval := p.renewInterval
	if interval <= 0 || interval >= p.ttl {
		interval = p.ttl / 2
	}
	if interval <= 0 {
		interval = time.Second
	}
	renewCtx, cancelRenew := context.WithCancel(contextx.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := p.renewWithRetry(renewCtx, key, leaseToken); err != nil {
					if renewCtx.Err() != nil {
						lost.Close(nil)
						return
					}
					lost.Close(err)
					return
				}
			case <-renewCtx.Done():
				lost.Close(nil)
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancelRenew()
			wait := time.NewTimer(p.stopRenewTimeout())
			defer wait.Stop()
			select {
			case <-done:
			case <-wait.C:
				lost.Close(nil)
				p.logger.Warn(contextx.Background(), "lock renewal stop timed out",
					logging.String("key", key),
					logging.Duration("timeout", p.stopRenewTimeout()),
				)
			}
		})
	}
}

func (p *SQLLockProvider) renewOnce(parent context.Context, key string, leaseToken string) error {
	if parent == nil {
		parent = contextx.Background()
	}
	ctx, cancel := context.WithTimeout(parent, p.renewTimeout())
	defer cancel()
	expires := time.Now().Add(p.ttl).UnixMilli()
	res, err := p.db.Exec(ctx,
		fmt.Sprintf("UPDATE %s SET expires_at_ms = ? WHERE lock_key = ? AND owner = ? AND lease_token = ?", p.quotedTableName()),
		expires, key, p.owner, leaseToken,
	)
	if err != nil {
		// 与获取路径共用同一分类器：以返回错误链为准，恰好同时发生的取消
		// 不得覆盖无关的驱动错误。
		if classified := classifyLockError(err, "lock renewal failed", key); classified != nil {
			return classified
		}
		p.logger.Warn(ctx, "lock renewal failed", logging.Error(err), logging.String("key", key))
		return errors.NewCodeWithCause(errors.Database, "lock renewal failed", err).WithContext("key", key)
	}
	affected, aerr := res.RowsAffected()
	if aerr != nil {
		p.logger.Warn(ctx, "lock renewal rows affected failed", logging.Error(aerr), logging.String("key", key))
		return errors.NewCodeWithCause(errors.Database, "lock renewal rows affected failed", aerr).WithContext("key", key)
	}
	if affected == 0 {
		p.logger.Warn(ctx, "lock renewal stopped (owner mismatch or released)", logging.String("key", key))
		return errors.NewCode(errors.Conflict, "lock lease lost").WithContext("key", key)
	}
	return nil
}

// renewWithRetry 在单个续租周期内对瞬时数据库错误做有界重试，仅当连续 maxRenewAttempts
// 次失败后才把错误上报为 lease lost；owner 失配（Conflict）立即返回，不重试。
func (p *SQLLockProvider) renewWithRetry(ctx context.Context, key string, leaseToken string) error {
	backoff := p.renewRetryBackoff()
	var lastErr error
	for attempt := 1; attempt <= maxRenewAttempts; attempt++ {
		err := p.renewOnce(ctx, key, leaseToken)
		if err == nil {
			return nil
		}
		// 锁确实被抢占或释放（owner/token 失配），立即报告，不重试。
		if errors.Code(err) == errors.Conflict {
			return err
		}
		// 续租被主动取消（释放/停止），交由上层按取消处理。
		if ctx.Err() != nil {
			return err
		}
		lastErr = err
		if attempt < maxRenewAttempts {
			p.logger.Warn(ctx, "lock renewal transient failure, retrying",
				logging.String("key", key),
				logging.Int("attempt", attempt),
				logging.Error(err))
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return lastErr
}

// renewRetryBackoff 把多次重试分布在单个续租周期内，避免等到下个周期才重试导致租约过期。
func (p *SQLLockProvider) renewRetryBackoff() time.Duration {
	backoff := p.renewInterval / time.Duration(maxRenewAttempts+1)
	if backoff <= 0 {
		backoff = 50 * time.Millisecond
	}
	if backoff > 500*time.Millisecond {
		backoff = 500 * time.Millisecond
	}
	return backoff
}

func (p *SQLLockProvider) renewTimeout() time.Duration {
	timeout := 5 * time.Second
	if p.ttl <= 0 {
		return timeout
	}
	if halfTTL := p.ttl / 2; halfTTL > 0 && halfTTL < timeout {
		timeout = halfTTL
	}
	if timeout <= 0 {
		return time.Second
	}
	return timeout
}

func (p *SQLLockProvider) stopRenewTimeout() time.Duration {
	timeout := p.renewTimeout()
	if p.ttl > 0 && p.ttl < timeout {
		timeout = p.ttl
	}
	if timeout <= 0 {
		return time.Second
	}
	return timeout
}

func (p *SQLLockProvider) releaseFunc(key string, leaseToken string, stopRenew func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if stopRenew != nil {
				stopRenew()
			}
			ctx, cancel := context.WithTimeout(contextx.Background(), 5*time.Second)
			defer cancel()
			res, err := p.db.Exec(ctx,
				fmt.Sprintf("DELETE FROM %s WHERE lock_key = ? AND owner = ? AND lease_token = ?", p.quotedTableName()),
				key, p.owner, leaseToken,
			)
			if err == nil {
				affected, aerr := res.RowsAffected()
				if aerr != nil {
					p.logger.Warn(ctx, "lock release rows affected failed", logging.Error(aerr), logging.String("key", key))
				} else if affected == 0 {
					p.logger.Warn(ctx, "lock release had no effect (owner mismatch or already expired)", logging.String("key", key))
				}
				return
			}
			p.logger.Warn(ctx, "lock release failed", logging.Error(err), logging.String("key", key))
		})
	}
}

var _ lock.ILockProvider = (*SQLLockProvider)(nil)
var _ lock.ILeaseLockProvider = (*SQLLockProvider)(nil)
