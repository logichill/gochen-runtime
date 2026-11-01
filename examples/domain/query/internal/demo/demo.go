// Package demo 提供 query examples 的共用底座。
//
// 这里故意不直接放一个完整可运行 main，而是抽出：
// - 共用实体
// - 共用 schema / request 解析
// - 共用内存查询仓储
// - 共用 range 匹配工具
//
// 这样上层两个示例只需要关注“自己想表达的那条主线”：
// 1. inferred/    -> 默认推导，QueryFilters 直通仓储
// 2. filter/      -> 业务自定义 filter struct，再由 querybind 绑定
package demo

import (
	"gochen/domain/spec"
	"context"
	"log"
	"net/http/httptest"
	"sort"
	"strings"
	"time"

	"gochen-runtime/api/rest"
	"gochen-runtime/http/nethttp"
	"gochen/app/crud"
	"gochen/app/query"
	"gochen/auth"
	"gochen/domain"
	"gochen/errors"
)

// User 是查询示例共用的最小实体。
//
// 字段刻意收敛到四类最常见查询能力：
// - Name: string like
// - Active: bool eq
// - Score: int range
// - CreatedAt: time range
//
// 两个 demo 都复用这份定义，避免“默认推导”和“业务自定义 filter”
// 各自维护一套几乎相同的样板实体。
type User struct {
	ID        int64     `json:"id" query:"nofilter,select"`
	Name      string    `json:"name"`
	Active    bool      `json:"active"`
	Score     int       `json:"score" query:"ops=eq|gte|lte,sort,select"`
	CreatedAt time.Time `json:"created_at" query:"ops=gte|lt|lte,sort,select"`
	Version   uint64    `json:"version" query:"-"`
}

func (u *User) GetID() int64       { return u.ID }
func (u *User) GetVersion() uint64 { return u.Version }

var _ domain.IEntity[int64] = (*User)(nil)

// ReadOnlyRepoBase 吸收 CRUD 仓储里与查询示例无关的方法，
// 让示例主体可以只聚焦 Query / QueryCount 这条主线。
type ReadOnlyRepoBase[T domain.IEntity[ID], ID comparable] struct{}

func (ReadOnlyRepoBase[T, ID]) Create(context.Context, T, spec.Spec) error {
	return errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) Update(context.Context, T, spec.Spec) error {
	return errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) Delete(context.Context, T, spec.Spec) error {
	return errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) Get(context.Context, ID, spec.Spec) (T, error) {
	var zero T
	return zero, errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) List(context.Context, int, int, spec.Spec) ([]T, error) {
	return nil, errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) Count(context.Context, spec.Spec) (int64, error) {
	return 0, errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) Exists(context.Context, ID, spec.Spec) (bool, error) {
	return false, errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) Query(context.Context, query.QueryOptions, spec.Spec) ([]T, error) {
	return nil, errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) QueryOne(context.Context, query.QueryOptions, spec.Spec) (T, error) {
	var zero T
	return zero, errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

func (ReadOnlyRepoBase[T, ID]) QueryCount(context.Context, query.QueryOptions, spec.Spec) (int64, error) {
	return 0, errors.NewCode(errors.Unsupported, "demo repo only supports query")
}

// QueryableUserRepo 是“默认推导 + QueryFilters 直通仓储”示例复用的只读仓储。
//
// 它故意直接消费 QueryFilters，模拟真实项目里 ORM/SQL builder
// 接住统一查询结构的过程。
type QueryableUserRepo struct {
	ReadOnlyRepoBase[*User, int64]
	Items []*User
}

func NewQueryableUserRepo(items []*User) *QueryableUserRepo {
	return &QueryableUserRepo{Items: items}
}

// Query 模拟“仓储直接接住结构化查询对象”的场景。
//
// 在真实 ORM/SQL 仓储里，这里通常会把：
// - opts.Filters -> where 条件
// - opts.Sorts   -> order by
// - opts.Offset/Limit -> 分页
// 翻译给底层查询构建器。
//
// 这个 demo 则故意在内存中做同样的事，目的是把数据流转看清楚。
func (r *QueryableUserRepo) Query(_ context.Context, opts query.QueryOptions, _ spec.Spec) ([]*User, error) {
	filtered, err := r.FilterOnly(opts.Filters)
	if err != nil {
		return nil, err
	}
	SortUsers(filtered, opts.Sorts)
	return PaginateUsers(filtered, opts.Offset, opts.Limit), nil
}

func (r *QueryableUserRepo) QueryOne(ctx context.Context, opts query.QueryOptions, spec spec.Spec) (*User, error) {
	items, err := r.Query(ctx, opts, spec)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.NewCode(errors.NotFound, "user not found")
	}
	return items[0], nil
}

func (r *QueryableUserRepo) QueryCount(_ context.Context, opts query.QueryOptions, _ spec.Spec) (int64, error) {
	filtered, err := r.FilterOnly(opts.Filters)
	if err != nil {
		return 0, err
	}
	return int64(len(filtered)), nil
}

// FilterOnly 把“是否命中过滤条件”的逻辑独立出来，
// 这样 Query 和 QueryCount 都能复用同一套过滤判断。
func (r *QueryableUserRepo) FilterOnly(filters query.QueryFilters) ([]*User, error) {
	filtered := make([]*User, 0, len(r.Items))
	for _, item := range r.Items {
		matched, err := MatchesUser(item, filters)
		if err != nil {
			return nil, err
		}
		if matched {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func Must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// NewRouteConfig 返回两个 demo 共享的 query 配置。
//
// 这里刻意把 schema 推导放在共用层，是为了强调：
// “默认规则”其实来自实体字段本身，而不是业务路由手写一套 if/else。
func NewRouteConfig() *rest.RouteConfig[int64] {
	schema, err := query.InferQuerySchema[User](nil)
	Must(err)
	return rest.NewQueryRouteConfig[int64](schema, 2, 20)
}

// ParsePaginationOptions 模拟 adapter/router 层行为：
// 先构造 HTTP 请求，再复用 rest 的分页与查询 DSL 解析逻辑。
func ParsePaginationOptions(rawURL string, cfg *rest.RouteConfig[int64]) (*query.PaginationOptions, error) {
	ctx, err := newContext(rawURL)
	if err != nil {
		return nil, err
	}
	return rest.ParsePaginationOptions(ctx, cfg)
}

// ParseQueryParams 与 ParsePaginationOptions 类似，
// 但只解析 filter/sorts/fields，不关心分页参数。
func ParseQueryParams(rawURL string, cfg *rest.RouteConfig[int64]) (*query.QueryRequest, error) {
	ctx, err := newContext(rawURL)
	if err != nil {
		return nil, err
	}
	return rest.ParseQueryParams(ctx, cfg)
}

// SampleUsers 提供一组足够小、但能看出过滤效果的固定样本数据。
// 这能让两个 demo 的输出稳定，也让注释和输出一一对应。
func SampleUsers() []*User {
	return []*User{
		{ID: 1, Name: "Alice", Active: true, Score: 82, CreatedAt: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)},
		{ID: 2, Name: "Bob", Active: false, Score: 65, CreatedAt: time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC)},
		{ID: 3, Name: "Alicia", Active: true, Score: 95, CreatedAt: time.Date(2026, 4, 6, 8, 30, 0, 0, time.UTC)},
	}
}

// MatchesUser 展示 QueryFilters 进入仓储后最直接的消费方式。
// bool/string 继续逐条读取；ordered field 则通过 RangeFor 收口成显式区间。
func MatchesUser(user *User, filters query.QueryFilters) (bool, error) {
	// active 是最简单的标量条件：
	// router 已经把 TRUE/true 规范化成了 bool，不需要业务自己再解析字符串。
	for _, expr := range filters.Get("active") {
		if expr.Op == query.FilterOpEq && user.Active != expr.Value.Bool {
			return false, nil
		}
	}

	// name 演示“仍然保留 operator 语义”的读取方式。
	// 这里直接看 expr.Op，就能知道当前是 like、eq 还是别的操作。
	for _, expr := range filters.Get("name") {
		if expr.Op == query.FilterOpLike && !strings.Contains(strings.ToLower(user.Name), strings.ToLower(expr.Value.String)) {
			return false, nil
		}
	}

	// score 演示 ordered field 的另一种消费方式：
	// 不逐条看 gte/lte，而是让框架先组合成一个显式区间对象。
	scoreRange, ok, err := query.RangeFor[int](filters, "score")
	if err != nil {
		return false, err
	}
	if ok && !MatchesRange(user.Score, scoreRange, CompareInt) {
		return false, nil
	}

	// created_at 与 score 同理，只是这里的值类型换成了 time.Time。
	createdAtRange, ok, err := query.RangeFor[time.Time](filters, "created_at")
	if err != nil {
		return false, err
	}
	if ok && !MatchesRange(user.CreatedAt, createdAtRange, CompareTime) {
		return false, nil
	}

	return true, nil
}

// MatchesRange 是业务消费 query.Range[T] 的最小模板：
// 框架负责把多个表达式整理成区间，业务只需要提供比较函数。
func MatchesRange[T any](value T, rng query.Range[T], compare func(left, right T) int) bool {
	if rng.Lower != nil {
		cmp := compare(value, rng.Lower.Value)
		if rng.Lower.Inclusive {
			if cmp < 0 {
				return false
			}
		} else if cmp <= 0 {
			return false
		}
	}
	if rng.Upper != nil {
		cmp := compare(value, rng.Upper.Value)
		if rng.Upper.Inclusive {
			if cmp > 0 {
				return false
			}
		} else if cmp >= 0 {
			return false
		}
	}
	return true
}

func CompareInt(left, right int) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func CompareTime(left, right time.Time) int {
	switch {
	case left.Before(right):
		return -1
	case left.After(right):
		return 1
	default:
		return 0
	}
}

// SortUsers 只实现示例真正用到的排序字段。
// 目的不是做一个通用排序器，而是让读者把注意力集中在 QueryOptions 的结构上。
func SortUsers(users []*User, sorts []query.Sort) {
	if len(sorts) == 0 {
		return
	}
	sortRule := sorts[0]
	sort.Slice(users, func(i, j int) bool {
		switch sortRule.Field {
		case "score":
			if sortRule.Direction == query.DESC {
				return users[i].Score > users[j].Score
			}
			return users[i].Score < users[j].Score
		case "created_at":
			if sortRule.Direction == query.DESC {
				return users[i].CreatedAt.After(users[j].CreatedAt)
			}
			return users[i].CreatedAt.Before(users[j].CreatedAt)
		default:
			return users[i].ID < users[j].ID
		}
	})
}

// PaginateUsers 模拟仓储层的 offset/limit 分页行为。
func PaginateUsers(users []*User, offset, limit int) []*User {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(users) {
		return []*User{}
	}
	if limit <= 0 || offset+limit > len(users) {
		limit = len(users) - offset
	}
	out := make([]*User, limit)
	copy(out, users[offset:offset+limit])
	return out
}

// NewApplication 构造只读查询应用；授权器在此显式选择，仓储能力在构造期绑定。
func NewApplication(repo *QueryableUserRepo) *crud.Application[*User, int64] {
	app, err := crud.NewReadApplication[*User, int64](repo, crud.Options[*User, int64]{
		Resource:   "user",
		Authorizer: auth.Allow(),
	})
	Must(err)
	return app
}

// newContext 只是为了在 example 里复用真正的 router 解码逻辑，
// 避免手工拼装 QueryRequest，导致示例脱离真实使用路径。
func newContext(rawURL string) (*nethttp.Context, error) {
	req := httptest.NewRequest("GET", rawURL, nil)
	return nethttp.NewBaseContext(httptest.NewRecorder(), req)
}
