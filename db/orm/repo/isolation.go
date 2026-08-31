package repo

import (
	"strings"

	"gochen/domain"
	"gochen/domain/crud"
	"gochen/errors"
)

// validateIsolationDeclaration 确保"声明了隔离"不会落空。
//
// WithIsolation 被调用即表示"这张表要隔离"。若该声明最终没能落到任何一列上，
// 结果是隔离条件一条都不加——调用方以为受保护，实际全表可见。这是 fail-closed
// 设计里最不该出现的失败模式，必须在构造期拦下，而不是安静地什么都不做。
//
// 唯一可能落空的组合：配置了 Resolve 却留空 Column。此时**不能**回落到
// tenant_id 默认值——自定义来源多半不是租户，悄悄写进租户列比报错更糟。
func (r *Repo[T, ID]) validateIsolationDeclaration() error {
	if !r.isolationDeclared || strings.TrimSpace(r.accessColumns.isolation) != "" {
		return nil
	}
	return errors.NewCode(errors.InvalidInput,
		"repository declares isolation with a custom IsolationCols.Resolve but no IsolationCols.Column; "+
			"the isolation key has no destination column (set Column explicitly; "+
			"it only defaults to \"tenant_id\" when Resolve is unset)").
		WithContext("resource_kind", r.resourceKind)
}

// validateIsolationEntity 在构造期校验：按**租户**隔离的实体必须能承载归属标记。
//
// §1.5.1 Fail-Closed 铁律：若实体未实现 domain/crud.ITenantEntity，
// Create 时无法写入租户归属，该行将永远落在任何租户的可见范围之外
// （或更糟——被写成空租户从而对所有人可见）。因此在 NewRepo 阶段就 fail-fast。
//
// 复用既有的 ITenantEntity 契约，不新建同义接口。
//
// 为什么只在**默认来源**下断言：配置了 IsolationCols.Resolve 就等于声明
// "本表的隔离键不是租户"，此时再要求实体实现 ITenantEntity 是张冠李戴——
// 按 org_id 隔离的实体没有理由必须带一个 TenantID 字段。这种情况下的归属
// 保障由 validateAccessColumns 承担：隔离列必须能映射到实体字段，否则构造期报错。
func validateIsolationEntity[T domain.IEntity[ID], ID comparable](isolationColumn string, customResolve bool) error {
	if strings.TrimSpace(isolationColumn) == "" {
		return nil // 未声明隔离 → 不做要求
	}
	if customResolve {
		return nil // 隔离键按定义不是租户 → 归属由列映射保障
	}
	var zero T
	if _, ok := any(zero).(crud.ITenantEntity[ID]); !ok {
		return errors.NewCode(errors.Unsupported,
			"repository isolates by tenant but entity does not implement domain/crud.ITenantEntity "+
				"(supply IsolationCols.Resolve if this repository isolates by something other than the tenant)").
			WithContext("isolation_column", isolationColumn)
	}
	return nil
}
