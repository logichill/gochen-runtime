package repo

import (
	"strings"

	"gochen/db/orm"
	"gochen/domain"
)

// NamedConditionFunc 把命名条件参数翻译成 SQL 条件。
//
// 命名条件只允许出现在读路径：写路径的 WITH CHECK 只能对实体自身求值，
// 因此写规约含 Named 时直接报错，不会进入本函数。
type NamedConditionFunc func(args []any) (orm.Condition, error)

// WithNamedCondition 为仓储注册一个命名条件翻译器。
//
// 注册只声明“这个名字存在”，供 domain/crud.INamedConditionProvider 暴露给
// Application 做构造期词汇校验；真正的 SQL 由翻译器生成。
func WithNamedCondition[T domain.IEntity[ID], ID comparable](name string, fn NamedConditionFunc) Option[T, ID] {
	name = strings.TrimSpace(name)
	return func(r *Repo[T, ID]) {
		if r == nil {
			return
		}
		if r.named == nil {
			r.named = make(map[string]NamedConditionFunc)
		}
		r.named[name] = fn
	}
}
