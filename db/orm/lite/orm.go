package lite

import (
	"context"
	"database/sql"
	"gochen-runtime/db/sql/sqlbuilder"
	"gochen/contextx"
	"gochen/db"
	"gochen/db/orm"
	"gochen/errors"
	"reflect"
	"sync"
)

// Option 用于配置 Orm 行为。
type Option func(*Orm)

// WithNamingConvention 配置 Orm 的全局命名规范。
func WithNamingConvention(naming db.NamingConvention) Option {
	return func(o *Orm) {
		o.naming = naming.WithDefaults()
	}
}

// Orm 定义Orm。
type Orm struct {
	db     db.IDatabase
	sql    sqlbuilder.ISQL
	caps   orm.Capabilities
	naming db.NamingConvention

	mu        sync.RWMutex
	structMap map[reflect.Type]*structMeta
}

// New 创建orm.IOrm。
func New(database db.IDatabase, opts ...Option) (orm.IOrm, error) {
	sql, err := sqlbuilder.New(database)
	if err != nil {
		return nil, err
	}
	var naming db.NamingConvention
	if provider, ok := database.(db.INamingConventionProvider); ok {
		naming = provider.NamingConvention()
	} else {
		naming = db.DefaultNamingConvention()
	}
	o := &Orm{
		db:  database,
		sql: sql,
		caps: orm.NewCapabilities(
			orm.CapabilityBasicCRUD,
			orm.CapabilityQuery,
			orm.CapabilityTransaction,
		),
		naming:    naming,
		structMap: make(map[reflect.Type]*structMeta),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}
	return o, nil
}

func (o *Orm) Capabilities() orm.Capabilities { return o.caps }

func (o *Orm) WithContext(ctx context.Context) orm.IOrm { // nolint: revive
	_ = ctx
	return o
}

func (o *Orm) Model(meta *orm.ModelMeta) (orm.IModel, error) {
	if meta == nil {
		return nil, errors.NewCode(errors.InvalidInput, "orm model meta cannot be nil")
	}

	table := meta.Table
	if table == "" {
		// 如果模型实现了 TableName()，优先使用
		if model := meta.NewModel(); model != nil {
			if tn, ok := tryGetTableName(model); ok {
				table = tn
			}
		}
	}
	if table == "" {
		return nil, errors.NewCode(errors.InvalidInput, "orm table name is empty")
	}

	return &model{
		orm:   o,
		meta:  meta,
		table: table,
	}, nil
}

// Begin 开启事务会话。
func (o *Orm) Begin(ctx context.Context) (orm.IOrmSession, error) {
	return o.BeginTx(ctx, nil)
}

// BeginTx 开启带选项的事务会话。
func (o *Orm) BeginTx(ctx context.Context, opts *sql.TxOptions) (orm.IOrmSession, error) {
	tx, err := o.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return o.wrapSession(tx)
}

func (o *Orm) wrapSession(tx db.ITransaction) (orm.IOrmSession, error) {
	txOrm, err := New(tx, WithNamingConvention(o.naming))
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	typed, ok := txOrm.(*Orm)
	if !ok {
		_ = tx.Rollback()
		return nil, errors.NewCode(errors.Internal, "unexpected orm type")
	}
	return &session{
		Orm:         typed,
		tx:          tx,
		afterCommit: contextx.NewAfterCommitDispatcher(),
	}, nil
}

func (o *Orm) Database() db.IDatabase { return o.db }

// NamingConvention 实现 db.INamingConventionProvider。
func (o *Orm) NamingConvention() db.NamingConvention {
	if o == nil {
		return db.DefaultNamingConvention()
	}
	return o.naming
}
