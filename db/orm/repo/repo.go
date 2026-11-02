package repo

import (
	"context"
	"reflect"
	"strings"

	"gochen-runtime/db/sql/safeident"
	"gochen/app"
	"gochen/db"
	"gochen/db/orm"
	"gochen/domain"
	"gochen/domain/audited"
	entitymeta "gochen/domain/meta"
	"gochen/errors"
	"gochen/gen"
)

// Repo 基于 gochen/db/orm 的通用仓储实现。
//
// 类型参数：
//   - T: 实体类型，需实现 domain.IEntity[ID]
//   - ID: 主键类型，需满足 comparable 约束（如 int64、string、uuid.UUID 等）
//
// 仓储只按规约执行：读取按规约过滤，写入在落库前以规约做 WITH CHECK；
// 普通 UPDATE 的 SET 排除有效契约中的受保护字段。
type Repo[T domain.IEntity[ID], ID comparable] struct {
	orm        orm.IOrm
	model      orm.IModel
	meta       *orm.ModelMeta
	entityMeta *entitymeta.Metadata
	resolver   *columnResolver
	idGen      gen.IGenerator[ID]

	resourceKind string
	table        string
	idColumn     string

	access    accessColumns
	contract  app.ResourceContract
	execution app.ExecutionDescriptor

	isolationDeclared   bool
	isolationConfigs    []IsolationCols
	isolationConflicted bool
	isolationResolve    IsolationResolveFunc

	crossIsolationReads bool
	crossIsolationAudit ICrossIsolationAudit

	softDelete      bool
	softDeleteCols  softDeleteColumns
	auditFields     bool
	settableVersion bool
	versioningOff   bool
	defaultActor    string

	maxPageSize   int
	readPredicate func(context.Context) (orm.Condition, error)
	named         map[string]NamedConditionFunc
}

type accessColumns struct {
	isolation      string
	isolationField entitymeta.Field
	version        string
	versionField   entitymeta.Field
}

type softDeleteColumns struct {
	DeletedAt string
	DeletedBy string
}

// NewRepo 创建仓储。
func NewRepo[T domain.IEntity[ID], ID comparable](ormEngine orm.IOrm, tableName string, opts ...Option[T, ID]) (*Repo[T, ID], error) {
	if ormEngine == nil || isNilORM(ormEngine) {
		return nil, errors.NewCode(errors.InvalidInput, "orm engine cannot be nil")
	}
	if session, ok := ormEngine.(orm.IOrmSession); ok && session != nil {
		if _, ok := orm.AfterCommitDispatcherFromSession(session); !ok {
			return nil, errors.NewCode(errors.Unsupported,
				"session-bound repo requires an after-commit-aware orm session to propagate post-commit callbacks")
		}
	}

	modelMeta := &orm.ModelMeta{
		ModelFactory: orm.NewModelFactory[T](),
		Table:        tableName,
	}
	r := &Repo[T, ID]{
		orm:          ormEngine,
		meta:         modelMeta,
		table:        tableName,
		resourceKind: tableName,
		defaultActor: "system",
		maxPageSize:  defaultMaxPageSize,
	}

	naming := resolveNamingConvention(ormEngine)

	// 能力探测（不依赖具体实体实例值）。
	var zero T
	if _, ok := any(zero).(domain.ISoftDeletable); ok {
		r.softDelete = true
	}
	if _, ok := any(zero).(audited.IAuditable); ok {
		r.auditFields = true
	}
	if _, ok := any(zero).(domain.ISettableVersion); ok {
		r.settableVersion = true
	}

	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	if r.isolationDeclared {
		var resolvedCol string
		var resolvedResolve IsolationResolveFunc
		first := true
		for _, cfg := range r.isolationConfigs {
			col := strings.TrimSpace(cfg.Column)
			if col == "" && cfg.Resolve == nil {
				col = naming.TenantColumn
			}
			if first {
				resolvedCol = col
				resolvedResolve = cfg.Resolve
				first = false
				continue
			}
			colDiff := resolvedCol != col
			var resolveDiff bool
			if (resolvedResolve == nil) != (cfg.Resolve == nil) {
				resolveDiff = true
			} else if resolvedResolve != nil && cfg.Resolve != nil {
				resolveDiff = reflect.ValueOf(resolvedResolve).Pointer() != reflect.ValueOf(cfg.Resolve).Pointer()
			}
			if colDiff || resolveDiff {
				r.isolationConflicted = true
				break
			}
		}
		r.access.isolation = resolvedCol
		r.isolationResolve = resolvedResolve
	}
	if r.isolationConflicted {
		return nil, errors.NewCode(errors.InvalidInput,
			"conflicting isolation configuration in repository options").
			WithContext("resource_kind", r.resourceKind)
	}
	if r.isolationDeclared && strings.TrimSpace(r.access.isolation) == "" {
		return nil, errors.NewCode(errors.InvalidInput,
			"repository declares isolation with a custom IsolationCols.Resolve but no IsolationCols.Column; "+
				"the isolation key has no destination column").
			WithContext("resource_kind", r.resourceKind)
	}

	metadata, err := entitymeta.Describe[T]()
	if err != nil {
		return nil, errors.Wrap(err, errors.InvalidInput, "entity metadata is invalid")
	}
	r.entityMeta = metadata

	model, err := ormEngine.Model(modelMeta)
	if err != nil {
		return nil, err
	}
	r.model = model
	if _, ok := model.(orm.IModelWithResult); !ok {
		return nil, errors.NewCode(errors.Unsupported,
			"repository requires an orm model with result support for constrained writes").
			WithContext("resource_kind", r.resourceKind)
	}
	resolver, err := newColumnResolver(metadata, model.Meta(), model.Dialect())
	if err != nil {
		return nil, err
	}
	r.resolver = resolver
	r.idColumn = resolver.primaryKeyColumn()
	if r.idColumn == "" {
		return nil, errors.NewCode(errors.InvalidInput,
			"entity must declare an identity field (entity:\"id\") so the repository can address rows").
			WithContext("resource_kind", r.resourceKind)
	}

	if r.access.isolation != "" {
		field, _, ok := resolver.resolveColumn(r.access.isolation)
		if !ok {
			return nil, errors.NewCode(errors.InvalidInput,
				"repository declares an isolation column that does not map to any entity field").
				WithContext("resource_kind", r.resourceKind).
				WithContext("column", r.access.isolation)
		}
		r.access.isolationField = field
		if r.contract.Isolation != (entitymeta.Ref{}) {
			configured, err := metadata.Resolve(r.contract.Isolation)
			if err != nil || configured.Name != field.Name {
				return nil, errors.NewCode(errors.InvalidInput, "conflicting isolation field binding")
			}
		}
		r.contract.Isolation = entitymeta.ByField(field.Name)
	}
	if r.contract.Isolation != (entitymeta.Ref{}) && r.access.isolation == "" {
		return nil, errors.NewCode(errors.InvalidInput, "isolation field requires an explicit isolation value resolver")
	}
	execution, err := app.BindContract(metadata, r.contract)
	if err != nil {
		return nil, err
	}
	r.execution = execution
	for _, field := range metadata.Fields() {
		if !execution.IsProtected(field) && field.Dimension == "" {
			continue
		}
		if _, ok := resolver.columnOf(field.Name); !ok {
			return nil, errors.NewCode(errors.InvalidInput, "contract field has no persisted column").WithContext("field", field.Name)
		}
	}
	idField, _ := metadata.IDField()
	if idField.Type != reflect.TypeOf((*ID)(nil)).Elem() {
		return nil, errors.NewCode(errors.InvalidInput, "entity identity type does not match repository ID")
	}
	if naming.VersionColumn != "" {
		if field, column, ok := resolver.resolveColumn(naming.VersionColumn); ok {
			r.access.version = column
			r.access.versionField = field
		}
	}
	if !r.versioningOff && r.access.version == "" {
		return nil, errors.NewCode(errors.InvalidInput,
			"repository requires an optimistic-lock version column; "+
				"map the naming-convention version column to an entity field or declare WithoutVersioning").
			WithContext("resource_kind", r.resourceKind).
			WithContext("column", naming.VersionColumn)
	}
	if r.auditFields && r.versioningOff {
		return nil, errors.NewCode(errors.InvalidInput,
			"audited repository cannot disable version protection (WithoutVersioning)").
			WithContext("resource_kind", r.resourceKind)
	}
	if !r.settableVersion && !r.versioningOff {
		return nil, errors.NewCode(errors.InvalidInput,
			"entity must implement domain.ISettableVersion or the repository must declare WithoutVersioning").
			WithContext("resource_kind", r.resourceKind)
	}
	if len(r.contract.Transferable) > 0 && !r.versioned() {
		return nil, errors.NewCode(errors.InvalidInput, "transfer contract requires optimistic versioning").
			WithContext("resource_kind", r.resourceKind)
	}

	if r.softDelete {
		if r.softDeleteCols.DeletedAt == "" {
			r.softDeleteCols.DeletedAt = naming.DeletedAtColumn
		}
		if r.softDeleteCols.DeletedAt == "" {
			return nil, errors.NewCode(errors.InvalidInput,
				"soft-deletable entity has no deleted_at column").
				WithContext("resource_kind", r.resourceKind)
		}
		if _, column, ok := resolver.resolveColumn(r.softDeleteCols.DeletedAt); ok {
			r.softDeleteCols.DeletedAt = column
		} else {
			return nil, errors.NewCode(errors.InvalidInput,
				"repository declares a soft delete column that does not map to any entity field").
				WithContext("resource_kind", r.resourceKind).
				WithContext("column", r.softDeleteCols.DeletedAt)
		}
		if r.auditFields && r.softDeleteCols.DeletedBy == "" {
			r.softDeleteCols.DeletedBy = naming.DeletedByColumn
		}
		if r.softDeleteCols.DeletedBy != "" {
			if _, column, ok := resolver.resolveColumn(r.softDeleteCols.DeletedBy); ok {
				r.softDeleteCols.DeletedBy = column
			} else {
				return nil, errors.NewCode(errors.InvalidInput,
					"repository declares an audited deleted_by column that does not map to any entity field").
					WithContext("resource_kind", r.resourceKind).
					WithContext("column", r.softDeleteCols.DeletedBy)
			}
		}
	}

	for name, fn := range r.named {
		if strings.TrimSpace(name) == "" || fn == nil {
			return nil, errors.NewCode(errors.InvalidInput, "named condition must have a name and translator").
				WithContext("resource_kind", r.resourceKind)
		}
	}
	if err := r.validateCrossIsolationDeclaration(); err != nil {
		return nil, err
	}
	if err := r.validateColumnIdentifiers(); err != nil {
		return nil, err
	}
	return r, nil
}

func resolveNamingConvention(ormEngine orm.IOrm) db.NamingConvention {
	if provider, ok := ormEngine.(db.INamingConventionProvider); ok {
		return provider.NamingConvention().WithDefaults()
	}
	if ormEngine.Database() != nil {
		if provider, ok := ormEngine.Database().(db.INamingConventionProvider); ok {
			return provider.NamingConvention().WithDefaults()
		}
	}
	return db.DefaultNamingConvention()
}

func (r *Repo[T, ID]) validateColumnIdentifiers() error {
	checks := []struct {
		name   string
		source string
	}{
		{r.idColumn, "primary_key"},
		{r.access.isolation, "role.tenant/isolation"},
		{r.access.version, "version"},
		{r.softDeleteCols.DeletedAt, "soft_delete.deleted_at"},
		{r.softDeleteCols.DeletedBy, "soft_delete.deleted_by"},
	}
	for _, check := range checks {
		if check.name == "" {
			continue
		}
		if err := safeident.ValidateQualifiedIdentifier(check.name, check.source, 1); err != nil {
			return errors.NewCode(errors.InvalidInput, "unsafe repository identifier").
				WithContext("identifier", check.name).
				WithContext("source", check.source)
		}
	}
	return nil
}

// DataSourceID 实现 domain/crud.IDataSourceProvider：返回底层 ORM 的数据源标识。
// 适配器未提供身份时返回空串，由 Application 构造期拒绝。
func (r *Repo[T, ID]) DataSourceID() string {
	if r == nil {
		return ""
	}
	id, _ := orm.DatasourceIDOf(r.orm)
	return id
}

// HasNamedCondition 实现 domain/crud.INamedConditionProvider。
func (r *Repo[T, ID]) HasNamedCondition(name string) bool {
	if r == nil {
		return false
	}
	_, ok := r.named[strings.TrimSpace(name)]
	return ok
}

func isNilORM(ormEngine orm.IOrm) bool {
	value := reflect.ValueOf(ormEngine)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// query 构造一个严格隔离的查询：写路径、写前探针都走这里。
func (r *Repo[T, ID]) query(ctx context.Context) (*queryBuilder, error) {
	model, err := r.ModelFor(ctx)
	if err != nil {
		return nil, err
	}
	return r.applyIsolation(newQueryBuilder(model, r.resolver, ctx), isolationStrict)
}

// readQuery 构造只读查询：满足跨隔离两道闸时可跳过隔离条件。
func (r *Repo[T, ID]) readQuery(ctx context.Context) (*queryBuilder, error) {
	model, err := r.ModelFor(ctx)
	if err != nil {
		return nil, err
	}
	quer, err := r.applyIsolation(newQueryBuilder(model, r.resolver, ctx), isolationRelaxable)
	if err != nil {
		return nil, err
	}
	if r.readPredicate != nil {
		condition, err := r.readPredicate(ctx)
		if err != nil {
			return nil, err
		}
		if condition.Expr != "" {
			quer.Where("("+condition.Expr+")", condition.Args...)
		}
	}
	return quer, nil
}

// ModelFor 返回上下文对应的模型（事务内复用事务会话的模型）。
func (r *Repo[T, ID]) ModelFor(ctx context.Context) (orm.IModel, error) {
	if session, ok := orm.SessionFromContext(ctx); ok && session != nil {
		if err := r.ensureSessionDatasource(session); err != nil {
			return nil, err
		}
		return session.Model(r.meta)
	}
	return r.model, nil
}

// ensureSessionDatasource 校验上下文中的事务会话是否属于本仓储绑定的数据源。
func (r *Repo[T, ID]) ensureSessionDatasource(session orm.IOrmSession) error {
	if r == nil {
		return nil
	}
	return ensureSessionDatasourceFor(r.orm, session, r.resourceKind)
}

// ensureSessionDatasourceFor 校验事务会话与给定 ORM 引擎的数据源归属。
//
// 规则（fail-closed）：
//   - 会话就是引擎本身（session-bound 模式）：直接放行；
//   - 两侧均提供身份且一致：放行；
//   - 两侧身份不一致，或任一侧无法提供身份：拒绝。
func ensureSessionDatasourceFor(engine orm.IOrm, session orm.IOrmSession, resource string) error {
	if session == nil {
		return nil
	}
	if sameOrmInstance(session, engine) {
		return nil
	}
	sessionID, sessionOK := orm.DatasourceIDOf(session)
	engineID, engineOK := orm.DatasourceIDOf(engine)
	if !sessionOK || !engineOK {
		return errors.NewCode(errors.FailedPrecondition,
			"cannot verify datasource ownership of the transaction session; the orm adapter must expose DatasourceID (db.IDatasourceIdentityProvider)").
			WithContext("resource_kind", resource)
	}
	if engineID != sessionID {
		return errors.NewCode(errors.FailedPrecondition,
			"transaction session belongs to a different datasource; open a separate transaction for this repository").
			WithContext("resource_kind", resource).
			WithContext("engine_datasource", engineID).
			WithContext("session_datasource", sessionID)
	}
	return nil
}

// sameOrmInstance 判断两个 ORM 值是否为同一实例。
func sameOrmInstance(a, b orm.IOrm) bool {
	if a == nil || b == nil {
		return false
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() || va.Kind() != reflect.Pointer {
		return false
	}
	return va.Pointer() == vb.Pointer()
}

// Model 暴露底层模型，供子类进行关联操作。
func (r *Repo[T, ID]) Model() orm.IModel { return r.model }

// Orm 返回绑定的 ORM 引擎。
func (r *Repo[T, ID]) Orm() orm.IOrm { return r.orm }

// ResourceKind 返回仓储用于授权与审计隔离的资源类型。
func (r *Repo[T, ID]) ResourceKind() string { return r.resourceKind }

// Association 返回关联操作入口。
func (r *Repo[T, ID]) Association(owner any, name string) orm.IAssociation {
	return r.model.Association(owner, name)
}

func (r *Repo[T, ID]) effectiveResourceKind() string {
	kind := strings.TrimSpace(r.resourceKind)
	if kind == "" {
		return r.table
	}
	return kind
}
