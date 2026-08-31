package repo

import (
	"context"
	"testing"

	"gochen/db/orm"

	"gochen/testkit/require"
)

type fakeModel struct {
	meta *orm.ModelMeta
}

// Meta result：返回的实例（类型：*orm.ModelMeta）。
//
// 返回：
func (m fakeModel) Meta() *orm.ModelMeta { return m.meta }

// Capabilities result：测试返回值（类型：orm.Capabilities）。
//
// 返回：
func (m fakeModel) Capabilities() orm.Capabilities { return nil }

func (m fakeModel) Dialect() repoTestDialect { return unknownRepoTestDialect() }

// First context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m fakeModel) First(context.Context, any, ...orm.QueryOption) error {
	panic("not implemented")
}

// Find 从存储中查询对象。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m fakeModel) Find(context.Context, any, ...orm.QueryOption) error { panic("not implemented") }

// Count 返回匹配条件的记录数。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (m fakeModel) Count(context.Context, ...orm.QueryOption) (int64, error) {
	panic("not implemented")
}

// Create 创建对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：可变参数（类型：...any）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m fakeModel) Create(context.Context, ...any) error { panic("not implemented") }

// Save context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m fakeModel) Save(context.Context, any, ...orm.QueryOption) error { panic("not implemented") }

// UpdateValues 更新对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - attrs：属性/参数集合（类型：map[string]any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m fakeModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	panic("not implemented")
}

// Delete 删除对象并同步到存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m fakeModel) Delete(context.Context, ...orm.QueryOption) error { panic("not implemented") }

// Association 执行对应操作。
//
// 参数：
// - s：参数值（具体语义见函数上下文）（类型：string）
//
// 返回：
// - result：测试返回值（类型：orm.IAssociation）
func (m fakeModel) Association(any, string) orm.IAssociation { panic("not implemented") }

// TestQueryBuilder_IsAllowedField 验证 QueryBuilder IsAllowedField。
func TestQueryBuilder_IsAllowedField(t *testing.T) {
	ctx := context.Background()

	t.Run("unsafe identifier", func(t *testing.T) {
		quer := newQueryBuilder(nil, ctx)
		require.False(t, quer.isAllowedField("name;drop table users"))
	})

	t.Run("nil model rejects dynamic field", func(t *testing.T) {
		quer := newQueryBuilder(nil, ctx)
		require.False(t, quer.isAllowedField("created_at"))
	})

	t.Run("nil meta rejects dynamic field", func(t *testing.T) {
		quer := newQueryBuilder(fakeModel{meta: nil}, ctx)
		require.False(t, quer.isAllowedField("created_at"))
	})

	t.Run("empty fields rejects dynamic field", func(t *testing.T) {
		quer := newQueryBuilder(fakeModel{meta: &orm.ModelMeta{Fields: nil}}, ctx)
		require.False(t, quer.isAllowedField("created_at"))
	})

	t.Run("whitelist by column or name", func(t *testing.T) {
		quer := newQueryBuilder(fakeModel{meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "CreatedAt", Column: "created_at"},
			},
		}}, ctx)

		require.True(t, quer.isAllowedField("created_at"))
		require.True(t, quer.isAllowedField("CreatedAt"))
		require.False(t, quer.isAllowedField("updated_at"))
		column, ok := quer.resolveAllowedColumn("CreatedAt")
		require.True(t, ok)
		require.Equal(t, "created_at", column)
	})
}
