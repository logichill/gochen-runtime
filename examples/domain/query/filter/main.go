package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gochen-runtime/examples/domain/query/internal/demo"
	"gochen/app/query"
	"gochen/app/query/querybind"
	"gochen/errors"
)

// textCondition 演示“业务自己保留 operator 语义”的场景。
// 默认规则只适合 eq/in 这类可安全推断的绑定；
// 如果业务需要像 like 这样保留操作符，就让字段自己实现 Decoder。
type textCondition struct {
	Value string
	Op    query.FilterOp
}

func (c *textCondition) DecodeQueryExprs(exprs []query.QueryExpr) error {
	if len(exprs) != 1 {
		return errors.NewCode(errors.InvalidInput, "text condition expects exactly one expression")
	}
	c.Value = exprs[0].Value.String
	c.Op = exprs[0].Op
	return nil
}

// userFilter 展示“业务定义 filter struct”后的形态：
// - Active 用默认规则绑定 eq
// - Name 用自定义 Decoder 保留 like
// - Score / CreatedAt 直接复用 query.Range[T]
//
// 这就是“默认规则 + 业务扩展点”那条路线的典型形态：
// - 简单字段不写额外代码
// - 复杂字段只在本字段上定义自己的解码规则
type userFilter struct {
	Active    *bool                  `filter:"active"`
	Name      *textCondition         `filter:"name"`
	Score     query.Range[int]       `filter:"score"`
	CreatedAt query.Range[time.Time] `filter:"created_at"`
}

// userQueryRepo 模拟一个“业务仓储”：
// 它不再接收通用 QueryFilters，而是接收业务自己定义好的 userFilter。
//
// 这更接近真实项目里的常见形态：
// - router 负责协议解析
// - service 负责业务编排
// - repo 只接收业务语义明确的查询对象
type userQueryRepo struct {
	items []*demo.User
}

func newUserQueryRepo(items []*demo.User) *userQueryRepo {
	return &userQueryRepo{items: items}
}

func (r *userQueryRepo) List(_ context.Context, filter userFilter) ([]*demo.User, error) {
	out := make([]*demo.User, 0, len(r.items))
	for _, user := range r.items {
		if matchesBusinessFilter(user, filter) {
			out = append(out, user)
		}
	}
	return out, nil
}

// userQueryService 模拟一个应用服务 / 读服务。
// 它承接 handler 绑定出来的业务 filter，然后把它交给 repo。
type userQueryService struct {
	repo *userQueryRepo
}

func newUserQueryService(repo *userQueryRepo) *userQueryService {
	return &userQueryService{repo: repo}
}

func (s *userQueryService) List(ctx context.Context, filter userFilter) ([]*demo.User, error) {
	return s.repo.List(ctx, filter)
}

// 这个示例不再让 repo 直接消费 QueryFilters，
// 而是先把统一查询结构绑定成业务自己的 filter struct。
//
// 适合这样的业务场景：
// - service/repo 希望拿到更语义化的业务对象
// - 某些字段要保留自己的 operator 解释方式
// - 想把默认绑定和特殊绑定混合使用
func main() {
	log.Println("=== Query Demo: business-defined filter struct ===")

	cfg := demo.NewRouteConfig()
	service := newUserQueryService(newUserQueryRepo(demo.SampleUsers()))

	// 这里仍然复用统一的 query DSL；
	// 变化只在于：后面不再直接读 QueryFilters，而是绑定到 userFilter。
	params, err := demo.ParseQueryParams(
		"/users?filter=active:eq:TRUE&filter=name:like:ali&filter=score:gte:80&filter=score:lte:100&filter=created_at:gte:2026-04-01T00:00:00Z&filter=created_at:lt:2026-04-07T00:00:00Z",
		cfg,
	)
	demo.Must(err)

	var filter userFilter
	// querybind 会按字段逐个处理：
	// - *bool 走默认规则
	// - *textCondition 调用自定义 Decoder
	// - Range[T] 使用 query.Range 自带的 DecodeQueryExprs
	demo.Must(querybind.Bind(params.Filters, &filter))

	users, err := service.List(context.Background(), filter)
	demo.Must(err)

	// 先把绑定结果打印出来，让“QueryFilters -> 业务 filter struct”的结果可见。
	fmt.Printf("active=%s\n", formatBool(filter.Active))
	fmt.Printf("name=%s\n", formatTextCondition(filter.Name))
	fmt.Printf("score=%s\n", formatIntRange(filter.Score))
	fmt.Printf("created_at=%s\n", formatTimeRange(filter.CreatedAt))

	// 再输出 service/repo 查询结果，让示例更像真实业务调用链。
	fmt.Println("matched users:")
	for _, user := range users {
		fmt.Printf("- id=%d name=%s\n", user.ID, user.Name)
	}
}

// matchesBusinessFilter 模拟 service/repo 在拿到“业务 filter struct”之后的使用方式。
// 这里已经不再关心 QueryExpr / FilterOp 列表，而是只处理业务想暴露的结构。
func matchesBusinessFilter(user *demo.User, filter userFilter) bool {
	if filter.Active != nil && user.Active != *filter.Active {
		return false
	}
	if filter.Name != nil && filter.Name.Op == query.FilterOpLike {
		if !strings.Contains(strings.ToLower(user.Name), strings.ToLower(filter.Name.Value)) {
			return false
		}
	}
	if !filter.Score.IsZero() && !demo.MatchesRange(user.Score, filter.Score, demo.CompareInt) {
		return false
	}
	if !filter.CreatedAt.IsZero() && !demo.MatchesRange(user.CreatedAt, filter.CreatedAt, demo.CompareTime) {
		return false
	}
	return true
}

// 打印辅助函数组
func formatBool(value *bool) string {
	if value == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%t", *value)
}

func formatTextCondition(cond *textCondition) string {
	if cond == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s:%s", cond.Op, cond.Value)
}

func formatIntRange(rng query.Range[int]) string {
	return fmt.Sprintf("lower=%s upper=%s", formatIntBound(rng.Lower), formatIntBound(rng.Upper))
}

func formatTimeRange(rng query.Range[time.Time]) string {
	return fmt.Sprintf("lower=%s upper=%s", formatTimeBound(rng.Lower), formatTimeBound(rng.Upper))
}

func formatIntBound(bound *query.Bound[int]) string {
	if bound == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d inclusive=%t", bound.Value, bound.Inclusive)
}

func formatTimeBound(bound *query.Bound[time.Time]) string {
	if bound == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s inclusive=%t", bound.Value.Format(time.RFC3339), bound.Inclusive)
}
