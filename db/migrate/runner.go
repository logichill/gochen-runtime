package migrate

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gochen-runtime/db/sql/safeident"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

const defaultStateTable = "schema_migrations"
const defaultMigrationType = "schema"
const lockMigrationType = "__gochen_migration_lock"
const defaultLockTimeout = 15 * time.Second
const defaultLockStaleAfter = defaultLockTimeout

// Status 描述当前 migration 状态。
type Status struct {
	Type    string
	Version uint64
	Dirty   bool
}

type runnerOptions struct {
	migrationType  string
	stateTable     string
	disableTx      bool
	lockTimeout    time.Duration
	lockStaleAfter time.Duration
	now            func() time.Time
}

// RunnerOption 用于配置 Runner。
type RunnerOption func(*runnerOptions)

// WithMigrationType 配置 Runner 管理的 migration 类型命名空间。
func WithMigrationType(name string) RunnerOption {
	return func(o *runnerOptions) {
		if strings.TrimSpace(name) != "" {
			o.migrationType = strings.TrimSpace(name)
		}
	}
}

// WithStateTable 配置 Runner 使用的状态表名称。
func WithStateTable(name string) RunnerOption {
	return func(o *runnerOptions) {
		if strings.TrimSpace(name) != "" {
			o.stateTable = strings.TrimSpace(name)
		}
	}
}

// WithoutTransaction 禁用单个 migration 文件的事务包装。
func WithoutTransaction() RunnerOption {
	return func(o *runnerOptions) {
		o.disableTx = true
	}
}

// WithLockTimeout 配置获取数据库级 migration 锁的等待时间。
func WithLockTimeout(timeout time.Duration) RunnerOption {
	return func(o *runnerOptions) {
		o.lockTimeout = timeout
	}
}

// WithLockStaleAfter 配置异常退出后 stale migration lock 的回收阈值。
func WithLockStaleAfter(staleAfter time.Duration) RunnerOption {
	return func(o *runnerOptions) {
		o.lockStaleAfter = staleAfter
	}
}

// Runner 提供最小可用的 migration 执行能力。
type Runner struct {
	db             db.IDatabase
	source         ISource
	dialect        dialect.IDialect
	migrationType  string
	stateTable     string
	disableTx      bool
	lockTimeout    time.Duration
	lockStaleAfter time.Duration
	lockToken      uint64
	lockStop       chan struct{}
	lockDone       chan struct{}
	lockErr        atomic.Pointer[lockHeartbeatFailure]
	withLockActive bool
	now            func() time.Time
	mu             sync.Mutex
}

// NewRunner 创建 migration runner。
func NewRunner(database db.IDatabase, source ISource, opts ...RunnerOption) (*Runner, error) {
	if database == nil {
		return nil, errors.NewCode(errors.InvalidInput, "database cannot be nil")
	}
	if source == nil {
		return nil, errors.NewCode(errors.InvalidInput, "migration source cannot be nil")
	}

	options := runnerOptions{
		migrationType:  defaultMigrationType,
		stateTable:     defaultStateTable,
		lockTimeout:    defaultLockTimeout,
		lockStaleAfter: defaultLockStaleAfter,
		now:            time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	if !safeident.IsSafeIdentifier(options.stateTable) {
		return nil, errors.NewCode(errors.InvalidInput, "invalid migration state table").
			WithContext("table", options.stateTable)
	}
	if !isSafeMigrationType(options.migrationType) {
		return nil, errors.NewCode(errors.InvalidInput, "invalid migration type").
			WithContext("type", options.migrationType)
	}
	if isReservedMigrationType(options.migrationType) {
		return nil, errors.NewCode(errors.InvalidInput, "migration type is reserved").
			WithContext("type", options.migrationType)
	}

	return &Runner{
		db:             database,
		source:         source,
		dialect:        dialect.FromDatabase(database),
		migrationType:  options.migrationType,
		stateTable:     options.stateTable,
		disableTx:      options.disableTx,
		lockTimeout:    options.lockTimeout,
		lockStaleAfter: options.lockStaleAfter,
		now:            options.now,
	}, nil
}

// List 返回 source 中按版本升序排列的 migration 集合。
func (r *Runner) List(ctx context.Context) ([]Migration, error) {
	ctx = normalizeContext(ctx)
	migrations, err := r.list(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Migration, len(migrations))
	copy(out, migrations)
	return out, nil
}

// Version 返回当前 migration 版本；没有已应用版本时返回 ErrNilVersion。
func (r *Runner) Version(ctx context.Context) (uint64, bool, error) {
	status, err := r.Status(ctx)
	if err != nil {
		return 0, false, err
	}
	if status.Version == 0 && !status.Dirty {
		return 0, false, ErrNilVersion
	}
	return status.Version, status.Dirty, nil
}

// Status 返回当前 migration 版本与 dirty 状态。
func (r *Runner) Status(ctx context.Context) (Status, error) {
	if err := r.beginExclusiveOperation("status"); err != nil {
		return Status{}, err
	}
	defer r.mu.Unlock()
	return r.status(ctx)
}

func (r *Runner) withLockedState(ctx context.Context, op string, fn func(ctx context.Context, state Status) error) error {
	if err := r.beginExclusiveOperation(op); err != nil {
		return err
	}
	defer r.mu.Unlock()

	ctx = normalizeContext(ctx)
	if err := r.ensureStateTable(ctx); err != nil {
		return err
	}
	if err := r.acquireLock(ctx); err != nil {
		return err
	}
	defer r.releaseLock()

	state, err := r.readStatus(ctx)
	if err != nil {
		return err
	}
	if state.Dirty {
		return dirtyStateError(state.Version)
	}
	return fn(ctx, state)
}

// Up 按顺序执行全部未执行的 up migration。
func (r *Runner) Up(ctx context.Context) error {
	return r.withLockedState(ctx, "up", func(ctx context.Context, state Status) error {
		return r.applyUpTo(ctx, state, 0, 0)
	})
}

// Steps 按指定步数迁移；n > 0 执行 up，n < 0 执行 down。
func (r *Runner) Steps(ctx context.Context, n int) error {
	if n == 0 {
		return ErrNoChange
	}
	if n > 0 {
		return r.stepsUp(ctx, n)
	}
	return r.stepsDown(ctx, -n)
}

// Migrate 迁移到指定版本。
func (r *Runner) Migrate(ctx context.Context, target uint64) error {
	return r.withLockedState(ctx, "migrate", func(ctx context.Context, state Status) error {
		if state.Version == target {
			return ErrNoChange
		}
		if target != 0 {
			if err := r.ensureTargetExists(ctx, target); err != nil {
				return err
			}
		}
		if target > state.Version {
			return r.applyUpTo(ctx, state, target, 0)
		}
		return r.applyDownTo(ctx, state, target, 0)
	})
}

// Down 回滚当前最新版本的一步 migration。
func (r *Runner) Down(ctx context.Context) error {
	return r.Steps(ctx, -1)
}

func (r *Runner) stepsUp(ctx context.Context, steps int) error {
	return r.withLockedState(ctx, "steps_up", func(ctx context.Context, state Status) error {
		return r.applyUpTo(ctx, state, 0, steps)
	})
}

func (r *Runner) stepsDown(ctx context.Context, steps int) error {
	return r.withLockedState(ctx, "steps_down", func(ctx context.Context, state Status) error {
		if state.Version == 0 {
			return ErrNilVersion
		}
		return r.applyDownTo(ctx, state, 0, steps)
	})
}

// Force 强制设置当前 migration 版本并清理 dirty 状态。
func (r *Runner) Force(ctx context.Context, version uint64) error {
	if err := r.beginExclusiveOperation("force"); err != nil {
		return err
	}
	defer r.mu.Unlock()

	ctx = normalizeContext(ctx)
	if err := r.ensureStateTable(ctx); err != nil {
		return err
	}
	if err := r.acquireLock(ctx); err != nil {
		return err
	}
	defer r.releaseLock()
	return r.writeStatus(ctx, Status{Version: version, Dirty: false})
}

// WithLock 在持有数据库级 migration 锁的前提下执行 fn。
func (r *Runner) WithLock(ctx context.Context, fn func() error) (err error) {
	if fn == nil {
		return errors.NewCode(errors.InvalidInput, "migration lock callback cannot be nil")
	}
	if err := r.beginExclusiveOperation("with_lock"); err != nil {
		return err
	}
	ctx = normalizeContext(ctx)
	if err := r.ensureStateTable(ctx); err != nil {
		r.mu.Unlock()
		return err
	}
	if err := r.acquireLock(ctx); err != nil {
		r.mu.Unlock()
		return err
	}
	r.withLockActive = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.withLockActive = false
		r.releaseLock()
		r.mu.Unlock()
		if err != nil {
			return
		}
		err = r.lockHeartbeatError()
	}()
	if err = fn(); err != nil {
		return err
	}
	return r.lockHeartbeatError()
}

// StateTableName 返回当前 Runner 使用的 migration 状态表名。
func (r *Runner) StateTableName() string {
	if r == nil {
		return ""
	}
	return r.stateTable
}

func (r *Runner) beginExclusiveOperation(operation string) error {
	r.mu.Lock()
	if !r.withLockActive {
		return nil
	}
	r.mu.Unlock()
	return errors.NewCode(errors.Conflict, "runner operation cannot be called while WithLock callback is active").
		WithContext("operation", operation)
}

func (r *Runner) quotedStateTable() string {
	return r.dialect.QuoteIdentifier(r.stateTable)
}

func (r *Runner) list(ctx context.Context) ([]Migration, error) {
	migrations, err := r.source.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Migration, 0, len(migrations))
	for _, migration := range migrations {
		migrationType := migration.Type
		if migrationType == "" {
			migrationType = defaultMigrationType
		}
		if migrationType == r.migrationType {
			out = append(out, migration)
		}
	}
	return out, nil
}

func rowsAffected(result sql.Result) (int64, error) {
	if result == nil {
		return 0, nil
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func waitForLockRetry(ctx context.Context, deadline time.Time, timeout time.Duration) error {
	if timeout <= 0 || !time.Now().Before(deadline) {
		return ErrLocked
	}
	wait := 10 * time.Millisecond
	remaining := time.Until(deadline)
	if remaining < wait {
		wait = remaining
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func boolToInt(flag bool) int {
	if flag {
		return 1
	}
	return 0
}

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return contextx.Background()
	}
	return ctx
}

func isMissingStateTableError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "no such table"),
		strings.Contains(text, "does not exist"),
		strings.Contains(text, "unknown table"):
		return true
	default:
		return false
	}
}
