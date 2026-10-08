package dialect

import (
	"fmt"
	coredialect "gochen/db/dialect"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"gochen-runtime/db/sqlscan"
	"gochen/db"
)

// DefaultMaxBindParameters 是单条语句可携带的占位参数默认上限。
//
// 批量操作（多值 INSERT、`id IN (...)`）必须按它分片：超限不是性能问题而是
// 运行期报错，因此这是**正确性**约束。取值是受支持方言的下界并留出余量：
//
//	| 方言       | 占位参数上限 |
//	| :--------- | :----------- |
//	| SQLite     | 32766（3.32 起；3.32 之前是 999） |
//	| MySQL      | 65535 |
//	| PostgreSQL | 65535 |
//
// **运行环境要求：SQLite ≥ 3.32。** Runtime 测试使用的 modernc.org/sqlite 远高于该版本；
// 接到更低上限的环境时用 db.DBConfig.MaxBindParameters 覆盖，
// 由数据库实现经 db.IBindParameterLimitProvider 回答。
//
// 这里不按方言分档：三个方言的下界已经宽到足够（默认批量上限 1000 行，
// 30000 个参数够 30 列的实体一次写完），分档只会换来一个用不上的 seam。
const DefaultMaxBindParameters = 30000

// MinMaxBindParameters 是 DBConfig.MaxBindParameters 可接受的下界。
//
// 设得过小不会报错，只会把每条语句切碎——一条 30 列的 INSERT 在预算 10 时
// 每次只能写一行，性能塌方却没有任何症状。因此在构造期就拒掉，
// 而不是让它静默降级。
const MinMaxBindParameters = 100

// ResolveMaxBindParameters 归一化占位参数预算：<=0 取默认值。
//
// 只负责取默认，不负责校验下界——非法配置必须在构造期 fail-fast
// （见 ValidateMaxBindParameters），不能在每次取值时被静默抹平。
func ResolveMaxBindParameters(configured int) int {
	if configured <= 0 {
		return DefaultMaxBindParameters
	}
	return configured
}

// ValidateMaxBindParameters 校验显式配置的占位参数预算，供数据库实现在构造期调用。
func ValidateMaxBindParameters(configured int) error {
	if configured == 0 {
		return nil
	}
	if configured < MinMaxBindParameters {
		return fmt.Errorf("max bind parameters must be 0 (use default %d) or at least %d, got %d",
			DefaultMaxBindParameters, MinMaxBindParameters, configured)
	}
	return nil
}

type standardDialect struct {
	name coredialect.Name
}

var databaseDialectCache sync.Map // map[db.IDatabase]coredialect.IDialect

// New 根据驱动名构造一个标准化的方言描述对象。
func New(name string) coredialect.IDialect {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "mysql":
		return standardDialect{name: coredialect.NameMySQL}
	case "sqlite", "sqlite3":
		return standardDialect{name: coredialect.NameSQLite}
	case "postgres", "postgresql":
		return standardDialect{name: coredialect.NamePostgres}
	default:
		return standardDialect{name: coredialect.NameUnknown}
	}
}

// FromDatabase 尝试从数据库适配器推断方言；无法识别时回退为 Unknown。
func FromDatabase(db db.IDatabase) coredialect.IDialect {
	if key, ok := databaseDialectCacheKey(db); ok {
		if cached, ok := databaseDialectCache.Load(key); ok {
			if d, ok := cached.(coredialect.IDialect); ok && d != nil {
				return d
			}
		}
		d := fromDatabase(db, 0)
		if d.Name() != coredialect.NameUnknown {
			databaseDialectCache.Store(key, d)
		}
		return d
	}
	return fromDatabase(db, 0)
}

func databaseDialectCacheKey(target db.IDatabase) (db.IDatabase, bool) {
	if target == nil {
		return nil, false
	}
	// Transactions are short-lived database handles. Caching them would retain
	// completed transactions (and their underlying driver state) for the life of
	// the process, while their dialect is already cheap to resolve directly.
	if _, ok := target.(db.ITransaction); ok {
		return nil, false
	}
	value := reflect.ValueOf(target)
	if !value.IsValid() || !value.Type().Comparable() {
		return nil, false
	}
	return target, true
}

func fromDatabase(target db.IDatabase, depth int) coredialect.IDialect {
	if target == nil {
		return standardDialect{name: coredialect.NameUnknown}
	}
	if p, ok := target.(coredialect.IDialectProvider); ok {
		if d := p.Dialect(); dialectAvailable(d) {
			return d
		}
	}
	if p, ok := target.(db.IDialectNameProvider); ok {
		return New(p.DialectName())
	}
	if depth >= 4 {
		return standardDialect{name: coredialect.NameUnknown}
	}
	if inner, ok := unwrapDatabase(target); ok {
		return fromDatabase(inner, depth+1)
	}
	if inner, ok := embeddedDatabase(target); ok {
		return fromDatabase(inner, depth+1)
	}
	return standardDialect{name: coredialect.NameUnknown}
}

func dialectAvailable(d coredialect.IDialect) bool {
	if d == nil {
		return false
	}
	value := reflect.ValueOf(d)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

type databaseUnwrapper interface {
	UnwrapDatabase() db.IDatabase
}

type databaseGetter interface {
	GetDB() db.IDatabase
}

type databaseUnderlying interface {
	UnderlyingDB() db.IDatabase
}

func unwrapDatabase(target db.IDatabase) (db.IDatabase, bool) {
	if p, ok := target.(databaseUnwrapper); ok {
		if inner := p.UnwrapDatabase(); inner != nil && !sameDatabase(inner, target) {
			return inner, true
		}
	}
	if p, ok := target.(databaseGetter); ok {
		if inner := p.GetDB(); inner != nil && !sameDatabase(inner, target) {
			return inner, true
		}
	}
	if p, ok := target.(databaseUnderlying); ok {
		if inner := p.UnderlyingDB(); inner != nil && !sameDatabase(inner, target) {
			return inner, true
		}
	}
	return nil, false
}

func sameDatabase(left db.IDatabase, right db.IDatabase) bool {
	if left == nil || right == nil {
		return left == right
	}
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if !leftValue.IsValid() || !rightValue.IsValid() {
		return !leftValue.IsValid() && !rightValue.IsValid()
	}
	if leftValue.Type() != rightValue.Type() || !leftValue.Type().Comparable() {
		return false
	}
	return leftValue.Interface() == rightValue.Interface()
}

func embeddedDatabase(target db.IDatabase) (db.IDatabase, bool) {
	value := reflect.ValueOf(target)
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil, false
	}
	dbType := reflect.TypeOf((*db.IDatabase)(nil)).Elem()
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		if !field.CanInterface() {
			continue
		}
		if !field.Type().Implements(dbType) {
			continue
		}
		inner, ok := field.Interface().(db.IDatabase)
		if ok && inner != nil && !sameDatabase(inner, target) {
			return inner, true
		}
	}
	return nil, false
}

func (d standardDialect) Name() coredialect.Name {
	return d.name
}

// QuoteIdentifier 按当前方言为表名或列名加上合适的引号。
func (d standardDialect) QuoteIdentifier(name string) string {
	if name == "" {
		return ""
	}
	parts := strings.Split(name, ".")
	for i, p := range parts {
		if p == "" {
			continue
		}
		switch d.name {
		case coredialect.NameMySQL:
			parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
		case coredialect.NameSQLite, coredialect.NamePostgres:
			parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
		default:
			// 未知方言：保持原样
		}
	}
	return strings.Join(parts, ".")
}

// Rebind 把通用 `?` 占位符转换成当前方言所需的形式。
func (d standardDialect) Rebind(query string) string {
	if query == "" {
		return query
	}
	switch d.name {
	case coredialect.NamePostgres:
		var sb strings.Builder
		sb.Grow(len(query) + 4)
		argIndex := 1
		prevScanner := sqlscan.NewPrevTokenScanner(query)
		for i := 0; i < len(query); {
			ch := query[i]
			if next, ok := sqlscan.ScanPostgresPlaceholderIgnored(query, i); ok {
				sb.WriteString(query[i:next])
				i = next
				continue
			}
			if ch == '?' && !sqlscan.IsPostgresJSONQuestionOperator(query, &prevScanner, i) {
				sb.WriteByte('$')
				sb.WriteString(strconv.Itoa(argIndex))
				argIndex++
			} else {
				sb.WriteByte(ch)
			}
			i++
		}
		return sb.String()
	default:
		return query
	}
}

func (d standardDialect) PlaceholderStyle() coredialect.PlaceholderStyle {
	if d.name == coredialect.NamePostgres {
		return coredialect.PlaceholderPostgres
	}
	return coredialect.PlaceholderQuestion
}

func (d standardDialect) SelectLockClause(skipLocked bool) (string, error) {
	switch d.name {
	case coredialect.NameMySQL, coredialect.NamePostgres:
		if skipLocked {
			return " FOR UPDATE SKIP LOCKED", nil
		}
		return " FOR UPDATE", nil
	default:
		// SQLite serializes writes at the database level and does not accept
		// FOR UPDATE. Unknown standard dialects historically behaved the same:
		// callers may still use their surrounding transaction and guarded update.
		return "", nil
	}
}

func (d standardDialect) LikeEscapeClause() string {
	if d.name == coredialect.NameMySQL {
		return " ESCAPE '\\\\'"
	}
	return " ESCAPE '\\'"
}

func (d standardDialect) SchemaFeatures() coredialect.SchemaFeatures {
	switch d.name {
	case coredialect.NameMySQL:
		return coredialect.SchemaFeatures{AutoIncrement: coredialect.AutoIncrementMySQL}
	case coredialect.NameSQLite:
		return coredialect.SchemaFeatures{
			AddNotNullColumnRequiresDefault: true,
			AutoIncrement:                   coredialect.AutoIncrementSQLite,
		}
	case coredialect.NamePostgres:
		return coredialect.SchemaFeatures{AutoIncrement: coredialect.AutoIncrementPostgresSerial}
	default:
		return coredialect.SchemaFeatures{}
	}
}

func (d standardDialect) CreateIndexTargets(table string, indexName string) (string, string) {
	if d.name != coredialect.NameSQLite {
		return indexName, table
	}
	schemaName, tableName := splitQualifiedName(table)
	if schemaName == "" {
		return indexName, tableName
	}
	return schemaName + "." + indexName, tableName
}

func (d standardDialect) DropIndexSQL(table string, indexName string) string {
	schemaName, _ := splitQualifiedName(table)
	switch d.name {
	case coredialect.NameMySQL:
		return fmt.Sprintf("DROP INDEX %s ON %s", d.QuoteIdentifier(indexName), d.QuoteIdentifier(table))
	case coredialect.NameSQLite:
		if schemaName != "" {
			return fmt.Sprintf("DROP INDEX %s", d.QuoteIdentifier(schemaName+"."+indexName))
		}
	case coredialect.NamePostgres:
		if schemaName != "" {
			return fmt.Sprintf("DROP INDEX %s.%s", d.QuoteIdentifier(schemaName), d.QuoteIdentifier(indexName))
		}
	}
	return fmt.Sprintf("DROP INDEX %s", d.QuoteIdentifier(indexName))
}

func (d standardDialect) MigrationStateUpsertSQL(table string) (string, bool) {
	switch d.name {
	case coredialect.NameSQLite, coredialect.NamePostgres:
		return fmt.Sprintf(
			"INSERT INTO %s (migration_type, version, dirty, updated_at) VALUES (?, ?, ?, ?) "+
				"ON CONFLICT (migration_type) DO UPDATE SET version = excluded.version, dirty = excluded.dirty, updated_at = excluded.updated_at",
			table,
		), true
	case coredialect.NameMySQL:
		return fmt.Sprintf(
			"INSERT INTO %s (migration_type, version, dirty, updated_at) VALUES (?, ?, ?, ?) "+
				"ON DUPLICATE KEY UPDATE version = VALUES(version), dirty = VALUES(dirty), updated_at = VALUES(updated_at)",
			table,
		), true
	default:
		return "", false
	}
}

func (d standardDialect) IntrospectionKey() string {
	return string(d.name)
}

func splitQualifiedName(name string) (string, string) {
	name = strings.TrimSpace(name)
	index := strings.IndexByte(name, '.')
	if index < 0 {
		return "", name
	}
	return strings.TrimSpace(name[:index]), strings.TrimSpace(name[index+1:])
}

// SupportsDeleteLimit 返回当前方言是否支持 `DELETE ... LIMIT`。
//
// 该能力取决于驱动的**编译期**特性而非方言名称：SQLite 的
// `DELETE ... LIMIT` 语法需要 SQLITE_ENABLE_UPDATE_DELETE_LIMIT 宏，
// 而Runtime 测试使用的 modernc.org/sqlite 未启用该宏，因此标准 SQLite 方言
// 返回 false（返回 true 会生成 SQLite 无法解析的语句）。
// 使用启用了该编译选项的自定义驱动时，可通过 coredialect.IDialectProvider 注入
// 自实现的 coredialect.IDialect 覆盖此能力声明。
func (d standardDialect) SupportsDeleteLimit() bool {
	switch d.name {
	case coredialect.NameMySQL:
		return true
	default:
		return false
	}
}

// SupportsSavepoints 当前方言是否支持 savepoint 语义。
func (d standardDialect) SupportsSavepoints() bool {
	switch d.name {
	case coredialect.NameMySQL, coredialect.NameSQLite, coredialect.NamePostgres:
		return true
	default:
		return false
	}
}

// IsUniqueViolation 判断是否发生了唯一键冲突。
//
// 检测逻辑统一委托给 db.IsUniqueViolation（结构化错误码优先，SQLSTATE/文本兜底），
// 避免各方言各自维护脆弱的文本匹配。
func (d standardDialect) IsUniqueViolation(err error) bool {
	return db.IsUniqueViolation(err)
}
