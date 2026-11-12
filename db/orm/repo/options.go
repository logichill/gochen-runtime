package repo

import (
	"context"
	"strings"

	"gochen/db/orm"
	"gochen/domain"
	"gochen/gen"
)

// Option 用于配置默认 ORM 仓储行为。
type Option[T domain.IEntity[ID], ID comparable] func(*Repo[T, ID])

// WithReadPredicate 为仓储读取追加可信业务条件，包括分页与计数。
// ModelFor 返回的原始模型不应用此条件，自定义查询须自行追加条件或使用 ScopedQuery。
// 此选项不授予写入权限。
func WithReadPredicate[T domain.IEntity[ID], ID comparable](predicate func(context.Context) (orm.Condition, error)) Option[T, ID] {
	return func(r *Repo[T, ID]) { r.readPredicate = predicate }
}

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

// IsolationResolveFunc 解析本次请求所属的隔离空间键。
//
// 返回空字符串等同"解析不到"，由调用方 fail-closed 拒绝；
// 返回 error 则直接上抛（解析器自身故障不得被当成"无隔离"）。
type IsolationResolveFunc func(ctx context.Context) (string, error)

// IsolationCols 声明空间隔离的落地方式。
//
// 零值即"按租户隔离"：`WithIsolation(IsolationCols{})` 等价于
// `IsolationCols{Column: "tenant_id"}` + 默认取 contextx.TenantID。
//
// 注意区分两个"默认"：**是否启用**隔离没有默认（不调用 WithIsolation 就完全不启用）；
// 一旦调用了 WithIsolation，就已经是显式声明了隔离意图，此时列名与取值来源才谈得上默认。
type IsolationCols struct {
	// Column 承载隔离键的列名，例 "tenant_id"、"org_id"。
	//
	// 留空时：未配置 Resolve → 默认取全局约定租户列（如 "tenant_id"）；配置了 Resolve → 构造期报错
	// （自定义了取值来源却不说往哪一列落，是不自洽的声明）。
	Column string

	// Resolve 解析本次请求的隔离键；为 nil 时默认取 contextx.TenantID。
	Resolve IsolationResolveFunc
}

// WithIsolation 显式启用空间隔离并声明其落地方式。
//
// 构造期校验（全部 fail-fast）：
//   - Column 必须能映射到实体字段，否则隔离条件会静默失效；
//   - 配置了 Resolve 却留空 Column → 报错；此时不能回落到 tenant_id 默认值，
//     否则分片键会被悄悄写进租户列；
//   - 隔离列绑定可转换隔离键的实体字段；未提供 Resolve 时取当前租户。
func WithIsolation[T domain.IEntity[ID], ID comparable](cols IsolationCols) Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		r.isolationDeclared = true
		r.isolationConfigs = append(r.isolationConfigs, cols)
		if col := strings.TrimSpace(cols.Column); col != "" {
			r.access.isolation = col
		}
		if cols.Resolve != nil {
			r.isolationResolve = cols.Resolve
		}
	}
}

// WithCrossIsolationReads 允许本仓储在**携带显式开闸凭据**的请求上跳过隔离条件。
//
// 两道闸必须同时合上才放行：
//  1. 本选项（构造期声明该仓储有被开闸的资格）；
//  2. contextx.WithCrossIsolation(ctx, ...) 注入的请求期凭据（带 Reason 与 Operator）。
//
// 构造期校验（全部 fail-fast）：
//   - audit 为 nil 即报错：无法留痕的阀门与"干脆不写 WithIsolation"没有区别；
//   - 未声明 WithIsolation 即报错：没有隔离可放，这个声明只会误导读代码的人。
//
// 不覆盖写入（在开闸请求上直接 Forbidden）。
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

// WithoutVersioning 显式退出 Update 的乐观锁版本保护。
//
// 默认策略：实体只要具备可写版本字段（嵌入 crud.Entity / audited.AuditedEntity）
// 或启用审计，Update 就以 WHERE id = ? AND version = ? 原子执行并推进版本。
// 本选项只服务于“表有版本列但业务明确不需要框架乐观锁”的场景。
//
// 构造期约束：启用审计的仓储不可退出（审计乐观锁依赖版本列），否则 NewRepo 报错。
func WithoutVersioning[T domain.IEntity[ID], ID comparable]() Option[T, ID] {
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		r.versioningOff = true
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
