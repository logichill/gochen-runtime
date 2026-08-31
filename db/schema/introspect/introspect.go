package introspect

import (
	"context"
	"strings"
	"sync"

	"gochen-runtime/db/schema"
	"gochen/db"
	"gochen/db/dialect"
	"gochen/errors"
)

var (
	introspectorMu        sync.RWMutex
	introspectorFactories = map[string]func() IIntrospector{
		string(dialect.NameSQLite):   func() IIntrospector { return NewSQLite() },
		string(dialect.NameMySQL):    func() IIntrospector { return NewMySQL() },
		string(dialect.NamePostgres): func() IIntrospector { return NewPostgres() },
	}
)

// IIntrospector 抽象数据库结构读取器。
type IIntrospector interface {
	// Inspect 读取数据库当前 schema 快照。
	Inspect(ctx context.Context, database db.IDatabase) (*schema.Schema, error)
}

// Register 为自定义方言注册 schema introspector factory。
func Register(key string, factory func() IIntrospector) error {
	key = strings.TrimSpace(key)
	if key == "" || factory == nil {
		return errors.NewCode(errors.InvalidInput, "introspector key and factory are required")
	}
	introspectorMu.Lock()
	defer introspectorMu.Unlock()
	introspectorFactories[key] = factory
	return nil
}

// Inspect 根据数据库方言选择默认 introspector。
func Inspect(ctx context.Context, database db.IDatabase) (*schema.Schema, error) {
	if database == nil {
		return nil, errors.NewCode(errors.InvalidInput, "database cannot be nil")
	}
	d := dialect.FromDatabase(database)
	key := strings.TrimSpace(d.IntrospectionKey())
	introspectorMu.RLock()
	factory := introspectorFactories[key]
	introspectorMu.RUnlock()
	if factory == nil {
		return nil, errors.NewCode(errors.Unsupported, "schema introspector is not registered").
			WithContext("dialect", d.Name()).
			WithContext("introspection_key", key)
	}
	inspector := factory()
	if inspector == nil {
		return nil, errors.NewCode(errors.Internal, "schema introspector factory returned nil").
			WithContext("introspection_key", key)
	}
	return inspector.Inspect(ctx, database)
}
