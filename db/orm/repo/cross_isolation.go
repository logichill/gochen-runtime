package repo

import (
	"context"
	"strings"

	"gochen/contextx"
	"gochen/errors"
)

// L2 跨隔离空间只读阀门。
//
// 为什么需要它：`applyIsolation` 一旦声明就无条件生效，而对账、计费汇总、
// 跨隔离空间报表、平台巡检这类需求客观存在。在没有正门之前，这些需求的唯一
// 出路是绕开仓储直接手写 SQL——那样不仅同样跨了隔离，还会一并丢掉软删除过滤、
// 字段映射与本文件的留痕。fail-closed 缺了配套开闸阀，实际效果往往不是更安全，
// 而是把开闸行为挤到框架看不见、也审计不到的地方。
//
// 为什么需要**两道闸**：光有构造期选项，等于这个仓储实例对所有请求永久敞开；
// 光有请求期凭据，等于任何中间层塞一个 ctx 值就能开闸。两者都必须具备才放行，
// 因此凭据泄漏到无关请求上不起作用，仓储被误配也不会自动敞开。
//
// 边界（三条铁律）：
//  1. 只放行读查询。写路径永不参与，且在已开闸仓储上直接拒绝（guardCrossIsolationWrite）；
//  2. 不放行资源边界解析。那是 L3 授权判定的输入，必须始终限定在本隔离空间内；
//  3. 留痕失败即放弃本次读取。记不下的开闸与静默开闸等价。

// CrossIsolationRead 描述一次**已放行**的跨隔离空间读取，用于强制留痕。
//
// 不含 SQL 文本：语句在此刻尚未渲染，且真正需要回答的审计问题是
// "谁为了什么在哪张表上跨了隔离"，而不是某一条具体语句长什么样。
type CrossIsolationRead struct {
	// ResourceKind 仓储声明的授权资源种类。
	ResourceKind string
	// Table 被跨隔离读取的物理表。
	Table string
	// IsolationColumn 本次被跳过的隔离列。
	IsolationColumn string
	// Reason 开闸原因，来自 contextx.CrossIsolation。
	Reason string
	// Operator 开闸发起人，来自 contextx.CrossIsolation。
	Operator string
}

// ICrossIsolationAudit 接收跨隔离只读放行的留痕。
//
// 错误契约：返回错误即中止本次读取。留痕是开闸的对价，不是可选的旁路观测。
type ICrossIsolationAudit interface {
	RecordCrossIsolationRead(ctx context.Context, read CrossIsolationRead) error
}

// CrossIsolationAuditFunc 允许用函数直接实现 ICrossIsolationAudit。
//
// 契约定义在选项旁边而不是独立包里（同 IsolationCols.Resolve 的口径）：
// L2 对 auth 包零依赖，把留痕出口挂到 auth/governance 上会
// 让 L0/L2 使用者重新背上治理包的编译依赖。下游要把它接进 governance、
// 日志或指标，在装配处转接即可。
type CrossIsolationAuditFunc func(ctx context.Context, read CrossIsolationRead) error

// RecordCrossIsolationRead 记录一次跨隔离只读放行。
func (f CrossIsolationAuditFunc) RecordCrossIsolationRead(ctx context.Context, read CrossIsolationRead) error {
	if f == nil {
		return errors.NewCode(
			errors.InvalidInput,
			"cross-isolation audit function cannot be nil; this indicates a wiring bug",
		)
	}
	return f(ctx, read)
}

// isolationPolicy 表达一次查询构造对隔离条件的态度。
type isolationPolicy int

const (
	// isolationStrict 隔离条件必须落地，是默认与兜底档：
	// 写路径、资源边界解析、以及任何未显式改用只读查询的路径都走这里。
	isolationStrict isolationPolicy = iota
	// isolationRelaxable 只读查询档：两道闸都合上时才跳过隔离条件。
	isolationRelaxable
)

// validateCrossIsolationDeclaration 拦下让开闸声明落空或无意义的配置。
func (r *Repo[T, ID]) validateCrossIsolationDeclaration() error {
	if !r.crossIsolationReads {
		return nil
	}
	if r.crossIsolationAudit == nil {
		return errors.NewCode(errors.InvalidInput,
			"repository allows cross-isolation reads but supplies no audit sink; "+
				"an unauditable valve is indistinguishable from silently omitting WithIsolation").
			WithContext("resource_kind", r.resourceKind)
	}
	if strings.TrimSpace(r.accessColumns.isolation) == "" {
		return errors.NewCode(errors.InvalidInput,
			"repository allows cross-isolation reads but declares no isolation to relax "+
				"(add WithIsolation, or drop WithCrossIsolationReads)").
			WithContext("resource_kind", r.resourceKind)
	}
	return nil
}

// crossIsolationRelaxed 判断本次读取是否满足两道闸，并在放行时完成留痕。
//
// 留痕先于放行：记不下就不放行，返回原始错误而不是降级为普通隔离读——
// 后者会让"审计沉默"表现为"一切正常"。
func (r *Repo[T, ID]) crossIsolationRelaxed(ctx context.Context, schema dataScopeSchema) (bool, error) {
	if !r.crossIsolationReads || schema.isolation.column == "" {
		return false, nil
	}
	credential, ok := contextx.CrossIsolationFrom(ctx)
	if !ok {
		return false, nil
	}
	if r.crossIsolationAudit == nil {
		// 构造期已拦，这里是纵深防御：宁可拒绝也不做无痕放行。
		return false, errors.NewCode(errors.Forbidden,
			"cross-isolation read requires an audit sink").
			WithContext("resource_kind", r.resourceKind)
	}
	if err := r.crossIsolationAudit.RecordCrossIsolationRead(ctx, CrossIsolationRead{
		ResourceKind:    r.resourceKind,
		Table:           r.meta.Table,
		IsolationColumn: schema.isolation.column,
		Reason:          credential.Reason,
		Operator:        credential.Operator,
	}); err != nil {
		return false, errors.Wrap(err, errors.Forbidden, "failed to record cross-isolation read")
	}
	return true, nil
}

// guardCrossIsolationWrite 在开闸期间拒绝本仓储上的一切写入。
//
// 为什么要显式报错，而不是让写路径按常规隔离静默继续：开着闸的 ctx 通常
// 根本没有隔离键（平台作业不属于任何隔离空间），此时写会以一条含义模糊的
// "requires an isolation key" 结束；而当它恰好带着某个隔离键时，一次本以为
// 是全局的批处理会静默只写中其中一个隔离空间。两种结局都比一条说清楚
// "开闸不含写权限"的错误更难排查。
//
// 只对**已声明允许开闸**的仓储生效：未声明的仓储上凭据本就是惰性的，
// 在同一个 ctx 里向别的仓储正常写入（例如把汇总结果写回某个隔离空间）
// 是合理用法，不该被牵连。
func (r *Repo[T, ID]) guardCrossIsolationWrite(ctx context.Context) error {
	if !r.crossIsolationReads {
		return nil
	}
	// 直接读 accessColumns 而不是 accessSchema()：写路径逐条调用，没必要为这层
	// 守卫再走一遍字段解析（构造期已保证声明了阀门就一定有隔离列）。
	isolationColumn := strings.TrimSpace(r.accessColumns.isolation)
	if isolationColumn == "" {
		return nil
	}
	credential, ok := contextx.CrossIsolationFrom(ctx)
	if !ok {
		return nil
	}
	return errors.NewCode(errors.Forbidden,
		"cross-isolation credential permits reads only; derive a per-isolation-space context before writing").
		WithContext("resource_kind", r.resourceKind).
		WithContext("isolation_column", isolationColumn).
		WithContext("reason", credential.Reason).
		WithContext("operator", credential.Operator)
}
