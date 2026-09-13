package repo

import (
	"context"
	"reflect"
	"strings"

	"gochen/auth/scoped"
	"gochen/db"
	"gochen/db/orm"
	"gochen/domain"
	"gochen/domain/audited"
	"gochen/errors"
	"gochen/gen"
)

// Repo 基于 gochen/db/orm 的通用仓储实现。
//
// 类型参数：
//   - T: 实体类型，需实现 domain.IEntity[ID]
//   - ID: 主键类型，需满足 comparable 约束（如 int64、string、uuid.UUID 等）
type Repo[T domain.IEntity[ID], ID comparable] struct {
	orm              orm.IOrm
	model            orm.IModel
	meta             *orm.ModelMeta
	idGen            gen.IGenerator[ID]
	resourceKind     string
	accessColumns    accessColumns
	isolationResolve IsolationResolveFunc
	isolationConfigs []IsolationCols
	// isolationDeclared 记录调用方是否显式调用过 WithIsolation，
	// 用于把"声明了却落不了地"的配置在构造期挑出来（见 validateIsolationDeclaration）。
	isolationDeclared   bool
	isolationConflicted bool
	scopeDeclared       bool
	// crossIsolationReads 记录本仓储是否有被开闸跨隔离只读的资格（见 cross_isolation.go）。
	crossIsolationReads bool
	crossIsolationAudit ICrossIsolationAudit
	dataScope           scoped.IDataScopeResolver
	softDelete          bool
	auditFields         bool
	defaultActor        string
	softDeleteCols      softDeleteColumns
	requireScope        bool
	allowGlobalScope    bool
	writeMissMode       WriteMissMode
	maxPageSize         int
}

// NewRepo 创建仓储。
func NewRepo[T domain.IEntity[ID], ID comparable](ormEngine orm.IOrm, tableName string, opts ...Option[T, ID]) (*Repo[T, ID], error) {
	if ormEngine == nil || isNilORM(ormEngine) {
		return nil, errors.NewCode(errors.InvalidInput, "orm engine cannot be nil")
	}
	if session, ok := ormEngine.(orm.IOrmSession); ok && session != nil {
		if _, ok := orm.AfterCommitDispatcherFromSession(session); !ok {
			return nil, errors.NewCode(errors.Unsupported,
				"session-bound repo requires an after-commit-aware orm session to propagate post-commit callbacks",
			)
		}
	}

	meta := &orm.ModelMeta{
		ModelFactory: orm.NewModelFactory[T](),
		Table:        tableName,
	}
	r := &Repo[T, ID]{
		orm:          ormEngine,
		meta:         meta,
		resourceKind: tableName,
		defaultActor: "system",
		maxPageSize:  defaultMaxPageSize,
	}

	// 提取全局规范约定（保证字段全量默认值归一化）
	var globalNaming db.NamingConvention
	if provider, ok := ormEngine.(db.INamingConventionProvider); ok {
		globalNaming = provider.NamingConvention().WithDefaults()
	} else if ormEngine.Database() != nil {
		if provider, ok := ormEngine.Database().(db.INamingConventionProvider); ok {
			globalNaming = provider.NamingConvention().WithDefaults()
		} else {
			globalNaming = db.DefaultNamingConvention()
		}
	} else {
		globalNaming = db.DefaultNamingConvention()
	}

	// 能力探测（不依赖具体实体实例值）
	var zero T
	if _, ok := any(zero).(domain.ISoftDeletable); ok {
		r.softDelete = true
	}
	if _, ok := any(zero).(audited.IAuditable); ok {
		r.auditFields = true
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
				col = globalNaming.TenantColumn
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
		r.accessColumns.isolation = resolvedCol
		r.isolationResolve = resolvedResolve
	}

	if r.isolationConflicted {
		return nil, errors.NewCode(errors.InvalidInput,
			"conflicting isolation configuration in repository options").
			WithContext("resource_kind", r.resourceKind)
	}

	if r.scopeDeclared {
		if r.accessColumns.managedScope == "" {
			r.accessColumns.managedScope = globalNaming.ScopeColumn
		}
		if r.accessColumns.version == "" {
			r.accessColumns.version = globalNaming.VersionColumn
		}
		if r.accessColumns.ownerID == "" && globalNaming.OwnerIDColumn != "" {
			typ := reflect.TypeOf(zero)
			for typ != nil && typ.Kind() == reflect.Ptr {
				typ = typ.Elem()
			}
			if _, ok := findFieldByColumn(typ, globalNaming.OwnerIDColumn, nil, map[reflect.Type]int{}); ok {
				r.accessColumns.ownerID = globalNaming.OwnerIDColumn
			}
		}
	}
	if r.softDelete {
		if r.softDeleteCols.DeletedAt == "" {
			r.softDeleteCols.DeletedAt = globalNaming.DeletedAtColumn
		}
		if r.auditFields && r.softDeleteCols.DeletedBy == "" {
			r.softDeleteCols.DeletedBy = globalNaming.DeletedByColumn
		}
	}

	switch r.writeMissMode {
	case WriteMissDiagnostic, WriteMissFastNotFound, WriteMissFastConflict:
	default:
		return nil, errors.NewCode(errors.InvalidInput, "invalid constrained write miss mode").
			WithContext("mode", r.writeMissMode)
	}
	if err := validateSemanticColumnTags[T](); err != nil {
		return nil, err
	}
	if err := r.validateColumnIdentifiers(); err != nil {
		return nil, err
	}
	if err := r.validateScopeEnforcement(); err != nil {
		return nil, err
	}
	if err := r.validateAccessColumns(); err != nil {
		return nil, err
	}
	if err := r.validateSoftDeleteColumns(); err != nil {
		return nil, err
	}
	if err := r.validateIsolationDeclaration(); err != nil {
		return nil, err
	}
	if err := validateIsolationEntity[T, ID](r.accessColumns.isolation, r.isolationResolve != nil); err != nil {
		return nil, err
	}
	if err := r.validateCrossIsolationDeclaration(); err != nil {
		return nil, err
	}

	model, err := ormEngine.Model(meta)
	if err != nil {
		return nil, err
	}
	r.model = model
	return r, nil
}

func (r *Repo[T, ID]) validateSoftDeleteColumns() error {
	if !r.softDelete {
		return nil
	}
	var zero T
	typ := reflect.TypeOf(zero)
	for typ != nil && typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	if r.softDeleteCols.DeletedAt != "" {
		if _, ok := findFieldByColumn(typ, r.softDeleteCols.DeletedAt, nil, map[reflect.Type]int{}); !ok {
			return errors.NewCode(errors.InvalidInput,
				"repository declares soft delete column that does not map to any entity field").
				WithContext("resource_kind", r.resourceKind).
				WithContext("column", r.softDeleteCols.DeletedAt)
		}
	}
	if r.auditFields && r.softDeleteCols.DeletedBy != "" {
		if _, ok := findFieldByColumn(typ, r.softDeleteCols.DeletedBy, nil, map[reflect.Type]int{}); !ok {
			return errors.NewCode(errors.InvalidInput,
				"repository declares audited deleted_by column that does not map to any entity field").
				WithContext("resource_kind", r.resourceKind).
				WithContext("column", r.softDeleteCols.DeletedBy)
		}
	}
	return nil
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

// query 构造一个**严格隔离**的查询：隔离条件必须落地。
//
// 这是默认档与兜底档——写路径、写前探针以及任何未显式改用 readQuery 的路径
// 都走这里，因此新增路径默认 fail-closed，忘记表态不会意外敞开。
func (r *Repo[T, ID]) query(ctx context.Context) (*queryBuilder, error) {
	model, err := r.ModelFor(ctx)
	if err != nil {
		return nil, err
	}
	return r.applyDataScope(newQueryBuilder(model, ctx), isolationStrict)
}

// readQuery 构造一个只读查询：满足跨隔离两道闸时可跳过隔离条件。
//
// 仅供把结果返回给调用方的读路径使用（见 cross_isolation.go）。
// L3 数据范围过滤不受影响，开闸只作用于 L2。
func (r *Repo[T, ID]) readQuery(ctx context.Context) (*queryBuilder, error) {
	model, err := r.ModelFor(ctx)
	if err != nil {
		return nil, err
	}
	return r.applyDataScope(newQueryBuilder(model, ctx), isolationRelaxable)
}

func (r *Repo[T, ID]) ModelFor(ctx context.Context) (orm.IModel, error) {
	if session, ok := orm.SessionFromContext(ctx); ok && session != nil {
		return session.Model(r.meta)
	}
	return r.model, nil
}

// Model 暴露底层模型，供子类进行关联操作。
func (r *Repo[T, ID]) Model() orm.IModel { return r.model }

// Orm 返回绑定的 ORM 引擎。
func (r *Repo[T, ID]) Orm() orm.IOrm { return r.orm }

// ResourceKind 返回仓储用于授权与审计隔离的资源类型。
func (r *Repo[T, ID]) ResourceKind() string { return r.writeResourceKind() }

func (r *Repo[T, ID]) Association(owner any, name string) orm.IAssociation {
	return r.model.Association(owner, name)
}

// ValidateWriteConstraintSupport 预检 constrained update/delete 所需的 ORM result 能力。
func (r *Repo[T, ID]) ValidateWriteConstraintSupport() error {
	if r == nil || r.model == nil {
		return errors.NewCode(errors.InvalidInput, "repo model is not initialized")
	}
	if _, ok := r.model.(orm.IModelWithResult); ok {
		return nil
	}
	return errors.NewCode(errors.Unsupported, "constrained update/delete requires an orm model with result support").
		WithContext("resource_kind", r.writeResourceKind())
}

type softDeleteColumns struct {
	DeletedAt string
	DeletedBy string
}
