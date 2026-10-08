package lite

import (
	"gochen-runtime/db/dialect"
	coredialect "gochen/db/dialect"
	"gochen/db/orm"
)

// ------------------------------------------------------------------------
// model 实现 orm.IModel
// ------------------------------------------------------------------------

type model struct {
	orm   *Orm
	meta  *orm.ModelMeta
	table string
}

func (m *model) Meta() *orm.ModelMeta { return m.meta }

func (m *model) Capabilities() orm.Capabilities { return m.orm.caps }

func (m *model) Dialect() coredialect.IDialect {
	return dialect.FromDatabase(m.orm.db)
}

// Association 当前仅提供占位实现，后续可按需扩展为多对多关联写入。
func (m *model) Association(owner any, name string) orm.IAssociation {
	return &unsupportedAssociation{name: name}
}
