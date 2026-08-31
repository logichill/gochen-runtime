package stdsql

import (
	"context"
	"database/sql"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
	"time"
)

// DB 基于 database/sql 的最小实现，满足 core.IDatabase 抽象。
type DB struct {
	db        *sql.DB
	driver    string
	dial      dialect.IDialect
	bindLimit int
	naming    *db.NamingConvention
}

// New 创建一个 `core.IDatabase`，并使用库层默认上下文完成初始化。
func New(config db.DBConfig) (db.IDatabase, error) {
	// 使用库层兜底 Context，避免重复创建底层 *sql.DB 连接。
	return NewWithContext(contextx.Background(), config)
}

// NewWithContext 创建数据库适配器，并在初始化时执行一次可用性探测。
func NewWithContext(ctx context.Context, config db.DBConfig) (db.IDatabase, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}

	var (
		driver = config.Driver
		dsn    = config.Database
	)
	if driver == "" {
		driver = "sqlite"
	}

	// 非法的占位参数预算在建连之前就拒掉：它不会导致报错，只会静默把每条
	// 批量语句切碎，属于最难从现象反推配置的一类问题。
	if err := dialect.ValidateMaxBindParameters(config.MaxBindParameters); err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "invalid database configuration")
	}

	sqlDB, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}

	// 连接池配置（可选）
	if config.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(config.MaxOpenConns)
	}
	if config.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(config.MaxIdleConns)
	}
	if config.ConnMaxLifetime > 0 {
		sqlDB.SetConnMaxLifetime(time.Duration(config.ConnMaxLifetime) * time.Second)
	}
	if config.ConnMaxIdleTime > 0 {
		sqlDB.SetConnMaxIdleTime(time.Duration(config.ConnMaxIdleTime) * time.Second)
	}

	// 基础可用性检查
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	var naming *db.NamingConvention
	if config.Naming != nil {
		normalized := config.Naming.WithDefaults()
		naming = &normalized
	}

	return &DB{
		db:        sqlDB,
		driver:    driver,
		dial:      dialect.New(driver),
		bindLimit: dialect.ResolveMaxBindParameters(config.MaxBindParameters),
		naming:    naming,
	}, nil
}

// Query 执行查询语句，并按当前方言重绑定占位符。
func (d *DB) Query(ctx context.Context, query string, args ...any) (db.IRows, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if d == nil || d.db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "db is nil")
	}
	q := d.dial.Rebind(query)
	rows, err := d.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return &Rows{rows: rows}, nil
}

// QueryRow 执行只期望返回单行结果的查询。
func (d *DB) QueryRow(ctx context.Context, query string, args ...any) db.IRow {
	if ctx == nil {
		return &Row{err: errors.NewCode(errors.InvalidInput, "ctx is nil")}
	}
	if d == nil || d.db == nil {
		return &Row{err: errors.NewCode(errors.InvalidInput, "db is nil")}
	}
	q := d.dial.Rebind(query)
	return &Row{row: d.db.QueryRowContext(ctx, q, args...)}
}

// Exec 执行非查询 SQL，并按当前方言重绑定占位符。
func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if d == nil || d.db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "db is nil")
	}
	q := d.dial.Rebind(query)
	return d.db.ExecContext(ctx, q, args...)
}

// Begin 以默认事务选项开启一个事务。
func (d *DB) Begin(ctx context.Context) (db.ITransaction, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if d == nil || d.db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "db is nil")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &Tx{
		db:        d.db,
		tx:        tx,
		dialect:   d.dial,
		bindLimit: d.bindLimit,
	}, nil
}

// BeginTx 按调用方提供的事务选项开启事务。
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (db.ITransaction, error) {
	if ctx == nil {
		return nil, errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if d == nil || d.db == nil {
		return nil, errors.NewCode(errors.InvalidInput, "db is nil")
	}
	tx, err := d.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{
		db:        d.db,
		tx:        tx,
		dialect:   d.dial,
		bindLimit: d.bindLimit,
	}, nil
}

// Ping 对底层数据库连接做一次连通性探测。
func (d *DB) Ping(ctx context.Context) error {
	if ctx == nil {
		return errors.NewCode(errors.InvalidInput, "ctx is nil")
	}
	if d == nil || d.db == nil {
		return errors.NewCode(errors.InvalidInput, "db is nil")
	}
	return d.db.PingContext(ctx)
}

// Close 关闭底层 `*sql.DB` 连接池。
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// SQLDB 返回底层 *sql.DB，仅供 stdsql 适配层使用。
func (d *DB) SQLDB() *sql.DB { return d.db }

// DialectName 返回当前底层驱动名，用于上层选择 SQL 方言。
func (d *DB) DialectName() string {
	return d.driver
}

// MaxBindParameters 实现 core.IBindParameterLimitProvider，供批量操作分片。
func (d *DB) MaxBindParameters() int {
	if d == nil {
		return dialect.DefaultMaxBindParameters
	}
	return dialect.ResolveMaxBindParameters(d.bindLimit)
}

// ExecDDL 直接执行 DDL 语句；它不会经过方言占位符重绑定。
func (d *DB) ExecDDL(sqlStmt string) error {
	if d.db == nil {
		return errors.NewCode(errors.InvalidInput, "db is nil")
	}
	_, err := d.db.Exec(sqlStmt)
	return err
}

// NamingConvention 实现 core.INamingConventionProvider。
func (d *DB) NamingConvention() db.NamingConvention {
	if d != nil && d.naming != nil {
		return *d.naming
	}
	return db.DefaultNamingConvention()
}
