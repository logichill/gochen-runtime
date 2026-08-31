package authz

import (
	"context"

	"gochen/auth/action"
	"gochen/errors"
)

// NewActionChecker 返回基于 ctx 中 Principal 的默认 L1 动作校验器。
//
// 存在意义：`Principal.AllowsPermission` 与三段式权限码的
// 通配匹配一直都在，缺的只是把它们接进 `action.IActionChecker` 的那一层。
// 缺了这一层，每个下游都要重写同一段判定，而它恰好是"写错就等于放行"的那段。
//
// 判定规则就是 Principal.AllowsPermission：IsSystem 主体直接放行，
// 显式持有或按段通配命中即放行，其余拒绝。
//
// 错误契约与 action.IActionChecker 声明的一致：
//   - 动作码非法 → errors.InvalidInput（装配期拼错，不是权限不足）；
//   - 主体缺失 → errors.Unauthorized；
//   - 主体已认证但无该动作 → errors.Forbidden。
//
// 需要更复杂的动作策略（按时间、按来源 IP、外接策略引擎）时换掉整个
// IActionChecker 实现即可，本实现不提供扩展点——那些规则没有共性可沉淀。
func NewActionChecker() action.IActionChecker {
	return principalActionChecker{}
}

type principalActionChecker struct{}

var _ action.IActionChecker = principalActionChecker{}

// RequireAction 校验当前主体是否具备指定动作权限。
func (principalActionChecker) RequireAction(ctx context.Context, code string) error {
	if !action.IsValidCode(code) {
		return errors.NewCode(errors.InvalidInput, "invalid action code").
			WithContext("action", code)
	}
	principal, err := RequirePrincipal(ctx)
	if err != nil {
		return err
	}
	if !principal.AllowsPermission(code) {
		return errors.NewCode(errors.Forbidden, "action is not granted to this principal").
			WithContext("action", code).
			WithContext("subject_id", principal.SubjectID)
	}
	return nil
}
