package repo

import (
	"context"

	"gochen/auth/scoped"
	"gochen/errors"
)

// 受控约束通道在仓储侧的消费入口。
//
// 完整链路：
//
//	app/security/scoped 装饰器 —— PDP 判定后 scoped.WithConstraint(ctx, provider)
//	    ▼
//	基础 Application（crud / audited / eventsourced）—— 对约束零感知，原样透传 ctx
//	    ▼
//	本文件 constraintGate —— 声明了范围列的仓储在写路径 fail-closed 读取
//	    ▼
//	*WithConstraint 私有写实现 —— 合并进原子 WHERE
//
// 关键点：约束**不是**写方法的入参。仓储是否消费约束由构造期
// WithScope(...) 的显式声明决定，调用方无从绕过，也无从伪造。

// constraintGate 判定本次写操作是否必须走约束路径，并取出约束。
//
// 三种结果：
//   - (constraint, true, nil)：仓储声明了范围列且通道命中 → 走约束写路径；
//   - (零值, false, nil)：仓储未声明范围列（L0/L1/L2）→ 走普通写路径；
//   - (零值, false, err)：仓储声明了范围列但通道未命中 → fail-closed 拒绝。
//
// 第三种是本闸门存在的全部意义：声明了范围列却拿不到约束，说明这次写要么
// 没有经过 L3 PEP，要么脱离了原始 ctx 树（裸 goroutine / context.Background()），
// 此时静默放行等同于把行级授权整体丢弃。
func (r *Repo[T, ID]) constraintGate(ctx context.Context) (scoped.WriteConstraint, bool, error) {
	if r == nil || !r.HasScopeDeclaration() {
		return scoped.WriteConstraint{}, false, nil
	}
	entityType := r.writeResourceKind()
	constraint, ok := scoped.ConstraintFrom(ctx, entityType)
	if ok {
		return constraint, true, nil
	}
	return scoped.WriteConstraint{}, false, r.constraintMissError(ctx, entityType)
}

// constraintMissError 构造 fail-closed 错误，并区分两种未命中成因。
//
// 这两种成因的排查方向完全不同，混为一谈会让开发者在错误的地方找问题：
//   - 通道里根本没有 provider：装配漏了 L3 装饰器，或异步丢了派生 ctx；
//   - 有 provider 但实体类型不匹配：Hook 内写了别的实体（嵌套写防护生效）。
func (r *Repo[T, ID]) constraintMissError(ctx context.Context, entityType string) error {
	reason := "no write constraint provider in context; " +
		"the write did not pass through the L3 scoped decorator, or the derived context was lost (bare goroutine / context.Background())"
	if scoped.HasConstraintProvider(ctx) {
		reason = "write constraint provider does not cover this entity type; " +
			"a hook or nested write targeted a different entity than the one authorized"
	}
	// 诊断信息随错误上下文携带（§4.3 风险矩阵）：仓储自身不持有 logger，
	// 由上层错误处理链统一输出，避免为一条 debug 日志给仓储引入日志依赖。
	return errors.NewCode(errors.Forbidden, "write is not authorized by any write constraint").
		WithContext("entity_type", entityType).
		WithContext("reason", reason)
}
