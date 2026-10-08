package repo

import (
	"context"
	coredialect "gochen/db/dialect"
	"testing"
	"time"

	"gochen/testkit/require"

	"gochen-runtime/db/dialect"
	"gochen/app/query"
	"gochen/db/orm"
	"gochen/errors"
)

type repoTestDialect = coredialect.IDialect

func unknownRepoTestDialect() repoTestDialect { return dialect.New("") }

func mustParseRFC3339(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	require.NoError(t, err)
	return parsed
}

func singleQueryFilter(field string, expr query.QueryExpr) query.QueryFilters {
	var filters query.QueryFilters
	return filters.Append(field, expr)
}

type queryIfaceEntity struct {
	ID      int64
	Version uint64
}

// GetID 从存储中查询数据。
//
// 返回：
// - result：数量/计数
func (e *queryIfaceEntity) GetID() int64 { return e.ID }

// GetVersion 从存储中查询数据。
//
// 返回：
// - result：数量/计数
func (e *queryIfaceEntity) GetVersion() uint64 { return e.Version }

type capturingQueryModel struct {
	meta       *orm.ModelMeta
	d          coredialect.IDialect
	nilDialect bool

	lastFindOpts  orm.QueryOptions
	lastFirstOpts orm.QueryOptions
	lastCountOpts orm.QueryOptions
}

// Meta result：返回的实例（类型：*orm.ModelMeta）。
//
// 返回：
func (m *capturingQueryModel) Meta() *orm.ModelMeta { return m.meta }

// Capabilities result：测试返回值（类型：orm.Capabilities）。
//
// 返回：
func (m *capturingQueryModel) Capabilities() orm.Capabilities { return nil }

func (m *capturingQueryModel) Dialect() coredialect.IDialect {
	if m.nilDialect {
		return nil
	}
	if m.d == nil {
		return dialect.New("")
	}
	return m.d
}

// First ctx：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - dest：目标对象（用于写入/拷贝结果）（类型：any）
// - opts：可选参数/配置项
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *capturingQueryModel) First(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	_ = ctx
	_ = dest
	m.lastFirstOpts = orm.CollectQueryOptions(opts...)
	return nil
}

// Find 从存储中查询对象。
//
// 参数：
// - ctx：上下文（用于取消、超时与链路信息）
// - dest：目标对象（用于写入/拷贝结果）（类型：any）
// - opts：可选参数/配置项
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *capturingQueryModel) Find(ctx context.Context, dest any, opts ...orm.QueryOption) error {
	_ = ctx
	_ = dest
	m.lastFindOpts = orm.CollectQueryOptions(opts...)
	return nil
}

// Count 返回匹配条件的记录数。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - result1：数值结果
// - err：错误信息（nil 表示成功）
func (m *capturingQueryModel) Count(ctx context.Context, opts ...orm.QueryOption) (int64, error) {
	_ = ctx
	m.lastCountOpts = orm.CollectQueryOptions(opts...)
	return 0, nil
}

// Create 创建对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：可变参数（类型：...any）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *capturingQueryModel) Create(context.Context, ...any) error { panic("not used") }

// Save context.Context：上下文（用于取消、超时与链路信息）。
//
// 参数：
// - v：参数值（具体语义见函数上下文）（类型：any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *capturingQueryModel) Save(context.Context, any, ...orm.QueryOption) error {
	panic("not used")
}

// UpdateValues 更新对象并写入存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - attrs：属性/参数集合（类型：map[string]any）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *capturingQueryModel) UpdateValues(context.Context, map[string]any, ...orm.QueryOption) error {
	panic("not used")
}

// Delete 删除对象并同步到存储。
//
// 参数：
// - context.Context：上下文（用于取消、超时与链路信息）
// - args：查询选项（用于补充 WHERE/JOIN/分页/排序等）（类型：...orm.QueryOption）
//
// 返回：
// - err：错误信息（nil 表示成功）
func (m *capturingQueryModel) Delete(context.Context, ...orm.QueryOption) error { panic("not used") }

// Association 执行对应操作。
//
// 参数：
// - s：参数值（具体语义见函数上下文）（类型：string）
//
// 返回：
// - result：测试返回值（类型：orm.IAssociation）
func (m *capturingQueryModel) Association(any, string) orm.IAssociation { panic("not used") }

// TestRepo_Query_InvalidOrderByReturnsInvalidInput 验证 Repo Query InvalidOrderByReturnsInvalidInput。
func TestRepo_Query_InvalidOrderByReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Sorts: []query.Sort{{Field: "id;drop table t", Direction: query.DESC}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.OrderBy, "Find should not be called when order by is invalid")
}

// TestRepo_Query_ValidOrderByIsPassedToModel 验证 Repo Query ValidOrderByIsPassedToModel。
func TestRepo_Query_ValidOrderByIsPassedToModel(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Sorts: []query.Sort{{Field: "id", Direction: query.DESC}},
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.OrderBy, 1)
	require.Equal(t, "id", m.lastFindOpts.OrderBy[0].Column)
	require.True(t, m.lastFindOpts.OrderBy[0].Desc)
}

func TestScopedQuery_SelectExprUnsafeRequiresCapability(t *testing.T) {
	m := &capturingQueryModel{}
	quer := newScopedQuery(newQueryBuilder(m, context.Background()))

	err := quer.SelectExprUnsafe(nil, "COUNT(*) AS total").
		Where("active = ?", true).
		Select("id").
		Find(&[]queryIfaceEntity{})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.Where)
	require.Empty(t, m.lastFindOpts.Select)
}

func TestScopedQuery_SelectExprUnsafeWithCapabilityPassesRawSelect(t *testing.T) {
	m := &capturingQueryModel{}
	quer := newScopedQuery(newQueryBuilder(m, context.Background()))

	err := quer.SelectExprUnsafe(AllowUnsafeSelectExpr(), "COUNT(*) AS total").Find(&[]queryIfaceEntity{})

	require.NoError(t, err)
	require.Equal(t, []string{"COUNT(*) AS total"}, m.lastFindOpts.SelectRaw)
	require.Empty(t, m.lastFindOpts.Select)
}

func TestScopedQuery_FirstPropagatesStoredErr(t *testing.T) {
	m := &capturingQueryModel{}
	quer := newScopedQuery(newQueryBuilder(m, context.Background()))

	err := quer.SelectExprUnsafe(nil, "COUNT(*) AS total").First(&queryIfaceEntity{})

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFirstOpts.Select)
}

func TestScopedQuery_CountPropagatesStoredErr(t *testing.T) {
	m := &capturingQueryModel{}
	quer := newScopedQuery(newQueryBuilder(m, context.Background()))

	_, err := quer.SelectExprUnsafe(nil, "COUNT(*) AS total").Count()

	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastCountOpts.Select)
}

func TestRepo_ListPage_EmptySortFieldReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.ListPage(context.Background(), &query.PaginationOptions{
		Page:  1,
		Size:  10,
		Sorts: []query.Sort{{Field: " ", Direction: query.DESC}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.OrderBy, "Find should not be called when sort field is empty")
}

func TestRepo_Query_EmptySortFieldReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Sorts: []query.Sort{{Field: " ", Direction: query.DESC}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.OrderBy, "Find should not be called when sort field is empty")
}

func TestRepo_QueryOne_EmptySortFieldReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.QueryOne(context.Background(), query.QueryOptions{
		Sorts: []query.Sort{{Field: " ", Direction: query.DESC}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFirstOpts.OrderBy, "First should not be called when sort field is empty")
}

func TestRepo_ListPage_TrimsSelectFieldsLikeQuery(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
				{Name: "Name", Column: "name"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.ListPage(context.Background(), &query.PaginationOptions{
		Page:   1,
		Size:   10,
		Fields: []string{" id ", " ", "name"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"id", "name"}, m.lastFindOpts.Select)
}

func TestRepo_ListPage_RejectsInvalidSelectFields(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
				{Name: "Name", Column: "name"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.ListPage(context.Background(), &query.PaginationOptions{
		Page:   1,
		Size:   10,
		Fields: []string{"id", "unknown", " name ", "unsafe-field;drop"},
	})
	require.Error(t, err)
	require.Truef(t, errors.Is(err, errors.InvalidInput), "expected invalid input, got %v", err)
	require.Nil(t, m.lastFindOpts.Select)
}

func TestRepo_ListPage_MapsSelectFieldNameToColumn(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.ListPage(context.Background(), &query.PaginationOptions{
		Page:   1,
		Size:   10,
		Fields: []string{"CreatedAt"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"created_at"}, m.lastFindOpts.Select)
}

func TestRepo_ListPage_ClampsDefaultMaxPageSize(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	got, err := r.ListPage(context.Background(), &query.PaginationOptions{
		Page: 1,
		Size: defaultMaxPageSize + 1,
	})
	require.NoError(t, err)
	require.Equal(t, defaultMaxPageSize, got.Size)
	require.Equal(t, defaultMaxPageSize, m.lastFindOpts.Limit)
}

func TestRepo_ListPage_ClampsConfiguredMaxPageSize(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m, maxPageSize: defaultMaxPageSize}
	WithMaxPageSize[*queryIfaceEntity, int64](25)(r)

	got, err := r.ListPage(context.Background(), &query.PaginationOptions{
		Page: 1,
		Size: 100,
	})
	require.NoError(t, err)
	require.Equal(t, 25, got.Size)
	require.Equal(t, 25, m.lastFindOpts.Limit)
}

// TestRepo_Query_MultiSortOrderIsPreserved 验证 Repo Query MultiSortOrderIsPreserved。
func TestRepo_Query_MultiSortOrderIsPreserved(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Sorts: []query.Sort{
			{Field: "created_at", Direction: query.DESC},
			{Field: "id", Direction: query.ASC},
		},
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.OrderBy, 2)
	require.Equal(t, "created_at", m.lastFindOpts.OrderBy[0].Column)
	require.True(t, m.lastFindOpts.OrderBy[0].Desc)
	require.Equal(t, "id", m.lastFindOpts.OrderBy[1].Column)
	require.False(t, m.lastFindOpts.OrderBy[1].Desc)
}

func TestRepo_Query_MapsSortFieldNameToColumn(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Sorts: []query.Sort{{Field: "CreatedAt", Direction: query.DESC}},
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.OrderBy, 1)
	require.Equal(t, "created_at", m.lastFindOpts.OrderBy[0].Column)
}

// TestRepo_QueryOne_InvalidOrderByReturnsInvalidInput 验证 Repo QueryOne InvalidOrderByReturnsInvalidInput。
func TestRepo_QueryOne_InvalidOrderByReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "ID", Column: "id"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.QueryOne(context.Background(), query.QueryOptions{
		Sorts: []query.Sort{{Field: "id desc", Direction: query.DESC}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFirstOpts.OrderBy, "First should not be called when order by is invalid")
}

// TestRepo_Query_UsesCriteriaFilters 验证 Repo Query UsesCriteriaFilters。
func TestRepo_Query_UsesCriteriaFilters(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Active", Column: "active"},
				{Name: "Age", Column: "age"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: func() query.QueryFilters {
			var filters query.QueryFilters
			filters = filters.Append("active", query.QueryExpr{
				Op:    query.FilterOpEq,
				Value: query.QueryValue{Type: query.FieldTypeBool, Normalized: "true", Bool: true},
			})
			filters = filters.Append("age", query.QueryExpr{
				Op: query.FilterOpIn,
				Values: []query.QueryValue{
					{Type: query.FieldTypeInt, Normalized: "18", Int: 18},
					{Type: query.FieldTypeInt, Normalized: "21", Int: 21},
				},
			})
			return filters
		}(),
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.Where, 2)
	require.Equal(t, "active = ?", m.lastFindOpts.Where[0].Expr)
	require.Len(t, m.lastFindOpts.Where[0].Args, 1)
	require.IsType(t, true, m.lastFindOpts.Where[0].Args[0])
	require.Equal(t, true, m.lastFindOpts.Where[0].Args[0])
	require.Equal(t, "age IN ?", m.lastFindOpts.Where[1].Expr)
	require.Len(t, m.lastFindOpts.Where[1].Args, 1)
	inArgs, ok := m.lastFindOpts.Where[1].Args[0].([]any)
	require.True(t, ok, "expected IN args to stay as slice")
	require.Equal(t, []any{int64(18), int64(21)}, inArgs)
}

func TestRepo_Query_MapsFilterFieldNameToColumn(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	filterTime := query.QueryValue{
		Type:       query.FieldTypeTime,
		Normalized: "2026-03-27T10:00:00Z",
		Time:       mustParseRFC3339(t, "2026-03-27T10:00:00Z"),
	}
	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("CreatedAt", query.QueryExpr{
			Op:    query.FilterOpGte,
			Value: filterTime,
		}),
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.Where, 1)
	require.Equal(t, "created_at >= ?", m.lastFindOpts.Where[0].Expr)
}

func TestRepo_Query_LikeEscapesWildcardChars(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Name", Column: "name"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("name", query.QueryExpr{
			Op:    query.FilterOpLike,
			Value: query.StringValue(`100%_match\done`),
		}),
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.Where, 1)
	require.Equal(t, "name LIKE ? ESCAPE '\\'", m.lastFindOpts.Where[0].Expr)
	require.Equal(t, []any{`%100\%\_match\\done%`}, m.lastFindOpts.Where[0].Args)
}

func TestRepo_Query_LikeUsesMySQLEscapeLiteral(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Name", Column: "name"},
			},
		},
		d: dialect.New("mysql"),
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("name", query.QueryExpr{
			Op:    query.FilterOpLike,
			Value: query.StringValue(`100%_match\done`),
		}),
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.Where, 1)
	require.Equal(t, "`name` LIKE ? ESCAPE '\\\\'", m.lastFindOpts.Where[0].Expr)
	require.Equal(t, []any{`%100\%\_match\\done%`}, m.lastFindOpts.Where[0].Args)
}

func TestRepo_Query_TrimsCriteriaFilterFieldBeforeLookup(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{{Name: "Status", Column: "status"}},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter(" status ", query.QueryExpr{
			Op:    query.FilterOpEq,
			Value: query.StringValue("active"),
		}),
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.Where, 1)
	require.Equal(t, "status = ?", m.lastFindOpts.Where[0].Expr)
	require.Equal(t, []any{"active"}, m.lastFindOpts.Where[0].Args)
}

func TestRepo_Query_BlankCriteriaFilterFieldReturnsRequiredError(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{{Name: "Status", Column: "status"}},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: query.QueryFilters{
			"   ": []query.QueryExpr{{
				Op:    query.FilterOpEq,
				Value: query.StringValue("active"),
			}},
		},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Contains(t, err.Error(), "query filter field is required")
	require.Empty(t, m.lastFindOpts.Where)
}

func TestRepo_Query_EmptyInFilterReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Status", Column: "status"},
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: func() query.QueryFilters {
			var filters query.QueryFilters
			filters = filters.Append("status", query.QueryExpr{Op: query.FilterOpIn})
			return filters
		}(),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.Where)
}

func TestQueryBuilder_EmptyAdapterInFilterReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Status", Column: "status"},
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	_, err := newQueryBuilder(m, context.Background()).withFilters([]query.Filter{
		{Field: "status", Op: query.FilterOpIn},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestQueryBuilder_EmptyAdapterNotInFilterReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Status", Column: "status"},
			},
		},
	}
	_, err := newQueryBuilder(m, context.Background()).withFilters([]query.Filter{
		{Field: "status", Op: query.FilterOpNotIn},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestQueryBuilder_InvalidAdapterFilterFieldReturnsError(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{{Name: "Status", Column: "status"}},
		},
	}

	_, err := newQueryBuilder(m, context.Background()).withFilters([]query.Filter{
		{Field: "status;drop table users", Op: query.FilterOpEq, Value: "active"},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
}

func TestQueryBuilder_AdapterLikeEscapesWildcardChars(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{{Name: "Name", Column: "name"}},
		},
	}

	quer, err := newQueryBuilder(m, context.Background()).withFilters([]query.Filter{
		{Field: "name", Op: query.FilterOpLike, Value: `_%\`},
	})
	require.NoError(t, err)
	opts := orm.CollectQueryOptions(quer.opts...)
	require.Len(t, opts.Where, 1)
	require.Equal(t, "name LIKE ? ESCAPE '\\'", opts.Where[0].Expr)
	require.Equal(t, []any{`%\_\%\\%`}, opts.Where[0].Args)
}

// TestRepo_QueryCount_UsesCriteriaFilters 验证 Repo QueryCount UsesCriteriaFilters。
func TestRepo_QueryCount_UsesCriteriaFilters(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	filterTime := query.QueryValue{
		Type:       query.FieldTypeTime,
		Normalized: "2026-03-27T10:00:00Z",
		Time:       mustParseRFC3339(t, "2026-03-27T10:00:00Z"),
	}
	_, err := r.QueryCount(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("created_at", query.QueryExpr{
			Op:    query.FilterOpGte,
			Value: filterTime,
		}),
	})
	require.NoError(t, err)
	require.Len(t, m.lastCountOpts.Where, 1)
	require.Equal(t, "created_at >= ?", m.lastCountOpts.Where[0].Expr)
	require.Len(t, m.lastCountOpts.Where[0].Args, 1)
	require.IsType(t, filterTime.Time, m.lastCountOpts.Where[0].Args[0])
	require.Equal(t, filterTime.Time, m.lastCountOpts.Where[0].Args[0])
}

func TestRepo_Query_UsesAdvancedFilters(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Status", Column: "status"},
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Advanced: query.AdvancedFilters{
			Or:        []query.OrCondition{{"status": "active"}},
			DateRange: &query.DateRangeExpr{Start: "2026-04-01T00:00:00Z"},
		},
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.Where, 2)
	require.Equal(t, "((status = ?))", m.lastFindOpts.Where[0].Expr)
	require.Equal(t, []any{"active"}, m.lastFindOpts.Where[0].Args)
	require.Equal(t, "created_at >= ?", m.lastFindOpts.Where[1].Expr)
	require.Equal(t, []any{"2026-04-01T00:00:00Z"}, m.lastFindOpts.Where[1].Args)
}

func TestRepo_Query_MapsAdvancedOrFieldNameToColumn(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Status", Column: "status"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Advanced: query.AdvancedFilters{
			Or: []query.OrCondition{{"Status": "active"}},
		},
	})
	require.NoError(t, err)
	require.Len(t, m.lastFindOpts.Where, 1)
	require.Equal(t, "((status = ?))", m.lastFindOpts.Where[0].Expr)
}

func TestRepo_Query_InvalidQueryFilterFieldReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{{Name: "Status", Column: "status"}},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("status;drop table users", query.QueryExpr{
			Op:    query.FilterOpEq,
			Value: query.StringValue("active"),
		}),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.Where)
}

func TestRepo_Query_InvalidQueryFilterValueTypeReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{{Name: "Status", Column: "status"}},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("status", query.QueryExpr{
			Op:    query.FilterOpEq,
			Value: query.QueryValue{Normalized: "active"},
		}),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.Where)
}

func TestRepo_Query_UnknownQueryFilterValueTypeReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{{Name: "Status", Column: "status"}},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Filters: singleQueryFilter("status", query.QueryExpr{
			Op:    query.FilterOpEq,
			Value: query.QueryValue{Type: query.FieldType("uuid"), Normalized: "0196"},
		}),
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Contains(t, err.Error(), "invalid query filter value type")
	require.Empty(t, m.lastFindOpts.Where)
}

func TestRepo_Query_AdvancedOrBlankFieldReturnsInvalidInput(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{{Name: "Status", Column: "status"}},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Advanced: query.AdvancedFilters{
			Or: []query.OrCondition{{"   ": "active"}},
		},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.Where)
}

func TestRepo_QueryCount_UsesAdvancedFilters(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Status", Column: "status"},
				{Name: "CreatedAt", Column: "created_at"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.QueryCount(context.Background(), query.QueryOptions{
		Advanced: query.AdvancedFilters{
			Or:        []query.OrCondition{{"status": "active"}},
			DateRange: &query.DateRangeExpr{End: "2026-04-30T23:59:59Z"},
		},
	})
	require.NoError(t, err)
	require.Len(t, m.lastCountOpts.Where, 2)
	require.Equal(t, "((status = ?))", m.lastCountOpts.Where[0].Expr)
	require.Equal(t, []any{"active"}, m.lastCountOpts.Where[0].Args)
	require.Equal(t, "created_at <= ?", m.lastCountOpts.Where[1].Expr)
	require.Equal(t, []any{"2026-04-30T23:59:59Z"}, m.lastCountOpts.Where[1].Args)
}

func TestRepo_Query_AdvancedDateRangeRequiresAllowedField(t *testing.T) {
	m := &capturingQueryModel{
		meta: &orm.ModelMeta{
			Fields: []orm.FieldMeta{
				{Name: "Status", Column: "status"},
			},
		},
	}
	r := &Repo[*queryIfaceEntity, int64]{model: m}

	_, err := r.Query(context.Background(), query.QueryOptions{
		Advanced: query.AdvancedFilters{
			DateRange: &query.DateRangeExpr{Start: "2026-04-01T00:00:00Z"},
		},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, errors.InvalidInput))
	require.Empty(t, m.lastFindOpts.Where)
}
