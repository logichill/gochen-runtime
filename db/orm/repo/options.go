package repo

import (
	"context"
	"strings"

	"gochen/auth/scoped"
	"gochen/domain"
	"gochen/gen"
)

// Option 用于配置默认 ORM 仓储行为。
type Option[T domain.IEntity[ID], ID comparable] func(*Repo[T, ID])

func WithIDGenerator[T domain.IEntity[ID], ID comparable](g gen.IGenerator[ID]) Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if g == nil {
			return
		}
		r.idGen = g
	}
}

// WithDefaultActor 配置默认操作人（当 ctx 中未携带 operator 时使用）。
func WithDefaultActor[T domain.IEntity[ID], ID comparable](actor string) Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if actor == "" {
			return
		}
		r.defaultActor = actor
	}
}

// WithSoftDeleteColumns 配置软删除列名（可选）。
func WithSoftDeleteColumns[T domain.IEntity[ID], ID comparable](deletedAtCol, deletedByCol string) Option[T, ID] {
	deletedAtCol = strings.TrimSpace(deletedAtCol)
	deletedByCol = strings.TrimSpace(deletedByCol)
	return func(r *Repo[T, ID]) {
		if r == nil || deletedAtCol == "" {
			return
		}
		r.softDelete = true
		r.softDeleteCols = softDeleteColumns{DeletedAt: deletedAtCol, DeletedBy: deletedByCol}
	}
}

// WithResourceKind 配置仓储对应的授权资源种类。
func WithResourceKind[T domain.IEntity[ID], ID comparable](kind string) Option[T, ID] {
	kind = strings.TrimSpace(kind)
	return func(r *Repo[T, ID]) {
		if r == nil || kind == "" {
			return
		}
		r.resourceKind = kind
	}
}

// WithDataScopeResolver 配置 repo 使用的数据边界解析器。
func WithDataScopeResolver[T domain.IEntity[ID], ID comparable](resolver scoped.IDataScopeResolver) Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil || resolver == nil {
			return
		}
		r.dataScope = resolver
	}
}

// IsolationResolveFunc 解析本次请求所属的隔离空间键。
//
// 返回空字符串等同"解析不到"，由调用方 fail-closed 拒绝；
// 返回 error 则直接上抛（解析器自身故障不得被当成"无隔离"）。
type IsolationResolveFunc func(ctx context.Context) (string, error)

// IsolationCols 声明 L2 空间隔离的落地方式。
//
// 零值即"按租户隔离"：`WithIsolation(IsolationCols{})` 等价于
// `IsolationCols{Column: "tenant_id"}` + 默认取 contextx.TenantID。
//
// 注意区分两个"默认"：**是否启用**隔离没有默认（不调用 WithIsolation 就完全不启用，
// 见 取代旧的按字段名隐式推断）；一旦调用了 WithIsolation，
// 就已经是显式声明了隔离意图，此时列名与取值来源才谈得上默认。
type IsolationCols struct {
	// Column 承载隔离键的列名，例 "tenant_id"、"org_id"。
	//
	// 留空时：未配置 Resolve → 默认取全局约定租户列（如 "tenant_id"）；配置了 Resolve → 构造期报错
	// （自定义了取值来源却不说往哪一列落，是不自洽的声明）。
	//
	// 命名说明：本字段是**机制层**概念（"哪一列承载隔离边界"），
	// 因此叫 Column 而不是 TenantID——租户只是它最常见的一种取值来源。
	Column string

	// Resolve 解析本次请求的隔离键；为 nil 时默认取 contextx.TenantID。
	//
	// 显式声明取值来源是这个字段存在的全部意义：Column 能换列名，
	// 若来源仍锁死在租户槽位，`Column: "org_id"` 就是个名不副实的承诺
	// （表按 org 隔离，值却从 tenant 取）。分片键、派生隔离键、
	// 非租户语义的隔离空间都靠它表达。
	Resolve IsolationResolveFunc
}

// ScopeCols 声明 L3 数据范围与资源边界依赖的列。
//
// 留空表示**不启用**范围过滤。
type ScopeCols struct {
	// ManagedScopeID 数据范围列名，例 "managed_scope_id"。
	ManagedScopeID string
	// OwnerID 资源归属列名（可选），用于资源边界解析。
	OwnerID string
	// Revision 乐观锁版本列名，例 "version"。
	Revision string
}

// WithIsolation 显式启用 L2 空间隔离并声明其落地方式。
//
// 零值即按租户隔离：`WithIsolation(IsolationCols{})` 等价于
// `IsolationCols{Column: "tenant_id"}`，取值来源为 contextx.TenantID。
//
// 构造期校验（全部 fail-fast）：
//   - Column 必须能映射到实体字段（见 validateAccessColumns），否则隔离条件会静默失效；
//   - 配置了 Resolve 却留空 Column → 报错。自定义了取值来源却不说往哪一列落，
//     是不自洽的声明；此时**不能**回落到 tenant_id 默认值，否则分片键会被
//     悄悄写进租户列；
//   - 未提供 Resolve（即隔离键就是租户）时，实体必须实现 domain/crud.ITenantEntity，
//     否则 NewRepo fail-fast（§1.5.1）。提供了自定义 Resolve 则不作此要求——
//     此时隔离键按定义就不是租户，归属由 Column 映射的字段承载。
//
// 注意本函数**不做**静默 no-op：调用它就是声明"这张表要隔离"，
// 任何让声明落空的配置都在构造期报错，而不是安静地什么都不加。
func WithIsolation[T domain.IEntity[ID], ID comparable](cols IsolationCols) Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		r.isolationDeclared = true
		r.isolationConfigs = append(r.isolationConfigs, cols)
		col := strings.TrimSpace(cols.Column)
		if col != "" {
			r.accessColumns.isolation = col
		}
		if cols.Resolve != nil {
			r.isolationResolve = cols.Resolve
		}
	}
}

// WithCrossIsolationReads 允许本仓储在**携带显式开闸凭据**的请求上跳过 L2 隔离条件。
//
// 这是 L2 唯一的正门，面向对账、计费汇总、跨隔离空间报表与平台巡检。
// 它刻意做得刺眼：装配图上看得见、每次放行都留痕、且只覆盖读。
//
// 两道闸必须同时合上才放行：
//  1. 本选项（构造期声明该仓储有被开闸的资格）；
//  2. contextx.WithCrossIsolation(ctx, ...) 注入的请求期凭据（带 Reason 与 Operator）。
//
// 缺任何一道都按常规隔离处理——凭据落到未声明本选项的仓储上完全惰性，
// 声明了本选项的仓储在无凭据请求上依然严格隔离。
//
// 构造期校验（全部 fail-fast）：
//   - audit 为 nil 即报错：无法留痕的阀门与"干脆不写 WithIsolation"没有区别；
//   - 未声明 WithIsolation 即报错：没有隔离可放，这个声明只会误导读代码的人。
//
// 不覆盖的路径：写入（在开闸请求上直接 Forbidden）与资源边界解析
// （L3 判定的输入必须始终限定在本隔离空间内）。
func WithCrossIsolationReads[T domain.IEntity[ID], ID comparable](audit ICrossIsolationAudit) Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		// audit 为 nil 时也要打开标记，交给 NewRepo 报错——静默丢弃整个声明
		// 会让调用方以为阀门已就位（实际它压根没开），是更坏的失败模式。
		r.crossIsolationReads = true
		r.crossIsolationAudit = audit
	}
}

// WithScope 显式启用 L3 数据范围过滤并声明其依赖列。
func WithScope[T domain.IEntity[ID], ID comparable](cols ScopeCols) Option[T, ID] {
	managedScope := strings.TrimSpace(cols.ManagedScopeID)
	ownerID := strings.TrimSpace(cols.OwnerID)
	revision := strings.TrimSpace(cols.Revision)
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		r.scopeDeclared = true
		if managedScope != "" {
			r.accessColumns.managedScope = managedScope
		}
		if ownerID != "" {
			r.accessColumns.ownerID = ownerID
		}
		if revision != "" {
			r.accessColumns.version = revision
		}
	}
}

// WithAudited 显式声明该仓储开启审计与软删除追踪。
func WithAudited[T domain.IEntity[ID], ID comparable]() Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		r.softDelete = true
		r.auditFields = true
	}
}

// WriteMissMode 决定约束写命中 0 行（RowsAffected == 0）时如何归因错误。
//
// 约束写的 WHERE 同时含 id / 租户 / 范围 / revision，命中 0 行有三种可能成因，
// 单靠写结果分不出来。区分它们需要一次额外的 SELECT 探针——这就是两种模式的取舍点。
type WriteMissMode int

const (
	// WriteMissDiagnostic 精准诊断模式（默认）：发起探针 SELECT，区分
	// NotFound（记录不存在或超出边界）与 Conflict（revision 不符）。
	//
	// 探针纯属**事后**错误分类，绝不参与前置安全判定，因此无 TOCTOU 风险。
	WriteMissDiagnostic WriteMissMode = iota

	// WriteMissFastNotFound 极速防探测模式：不发探针，一律返回 NotFound。
	//
	// 适合高并发只读为主、且不依赖乐观锁重试的场景：既省掉二次读 DB，
	// 又不向调用方泄露"记录存在但你无权"。代价是并发冲突也被报成 NotFound。
	WriteMissFastNotFound

	// WriteMissFastConflict 极速模式：不发探针，一律返回 Conflict。
	//
	// 适合写竞争激烈、客户端依赖 Conflict 触发重试的场景。代价是不存在的
	// 记录也被报成 Conflict，客户端会做一次无意义的重试。
	WriteMissFastConflict
)

// WithWriteMissMode 配置约束写未命中的错误归因模式。
//
// 不配置即 WriteMissDiagnostic（精准诊断）。极速模式牺牲归因精度换取
// 高并发下的二次读开销，属于部署期性能决策，故放在 Repo 构造期而非逐请求。
func WithWriteMissMode[T domain.IEntity[ID], ID comparable](mode WriteMissMode) Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		r.writeMissMode = mode
	}
}

// WithRequireScope 声明该仓储承载受租户/数据范围隔离的数据。
//
// 启用后：构造期要求实体具备 managed_scope_id 或 tenant_id 边界列，
// 否则 NewRepo fail-fast；运行期在 scoped 模式下无法施加任何边界条件时拒绝查询，
// 避免缺列实体静默退化为全局可见。配置型/字典型全局表不应启用此选项。
func WithRequireScope[T domain.IEntity[ID], ID comparable]() Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		r.requireScope = true
	}
}

// WithAllowGlobalScope 允许 require-scope 仓储在显式 Global data scope 下跳过 managed scope 过滤。
//
// 仅用于确认为平台级全局可见的数据访问；普通租户/范围隔离实体应保持默认 fail-closed。
func WithAllowGlobalScope[T domain.IEntity[ID], ID comparable]() Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		r.allowGlobalScope = true
	}
}

// WithMaxPageSize 配置 ListPage 允许的最大分页大小。
//
// n <= 0 时保留默认上限。
func WithMaxPageSize[T domain.IEntity[ID], ID comparable](n int) Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil || n <= 0 {
			return
		}
		r.maxPageSize = n
	}
}
