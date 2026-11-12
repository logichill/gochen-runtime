package main

import (
	"context"
	"fmt"
	"log"

	"gochen-runtime/api/rest"
	"gochen-runtime/examples/domain/query/internal/demo"
	"gochen/app/crud"
	"gochen/app/query"
	"gochen/domain/spec"
)

// 这个示例展示“默认推导”路径：
// 1. QuerySchema 由实体自动推导；
// 2. HTTP query 被框架解码成 PaginationOptions / QueryFilters；
// 3. CRUD application 直接把结构化查询条件透传给支持 QueryOptions 的仓储。
//
// 适合理解的顺序：
// - 先看 main：知道这个 demo 想证明什么
// - 再看 runQuery：看一条请求是怎样流过 adapter + application
// - 最后回到 internal/demo：看仓储如何消费 QueryFilters / Range
func main() {
	log.Println("=== Query Demo: inferred schema + direct QueryFilters ===")

	// 这一步相当于项目启动期：准备 schema、repo、application。
	cfg := demo.NewRouteConfig()
	repo, err := demo.NewQueryableUserRepo(demo.SampleUsers())
	demo.Must(err)
	app := demo.NewApplication(repo)

	// 正常查询：
	// - active:eq:true
	// - name:like:ali
	// - score 范围
	// - created_at 范围
	// - created_at 降序
	runQuery(repo, app, cfg,
		"/users?page=1&size=2&filter=active:eq:TRUE&filter=name:like:ali&filter=score:gte:80&filter=score:lte:100&filter=created_at:gte:2026-04-01T00:00:00Z&filter=created_at:lt:2026-04-07T00:00:00Z&sorts=created_at:desc")

	// 非法区间：
	// 下界比上界还大，框架会在 adapter 解码阶段直接报错，
	// 业务和仓储不会再拿到一个语义错误的范围对象。
	runQuery(repo, app, cfg,
		"/users?filter=created_at:gte:2026-04-08T00:00:00Z&filter=created_at:lt:2026-04-01T00:00:00Z")
}

// runQuery 把一条请求拆成两段看：
// 1. ParsePaginationOptions：HTTP query -> PaginationOptions
// 2. app.Query：PaginationOptions -> QueryOptions -> repo.Query
func runQuery(
	repo *demo.QueryableUserRepo,
	app *crud.ReadApplication[*demo.User, int64],
	cfg *rest.RouteConfig[int64],
	rawURL string,
) {
	pageReq, err := demo.ParsePaginationOptions(rawURL, cfg)
	if err != nil {
		fmt.Printf("query=%s\nerror=%v\n\n", rawURL, err)
		return
	}

	opts := query.QueryOptions{
		Offset:   pageReq.Offset(),
		Limit:    pageReq.Size,
		Fields:   pageReq.Fields,
		Sorts:    pageReq.Sorts,
		Filters:  pageReq.Filters,
		Advanced: pageReq.Advanced,
	}
	ctx := context.Background()
	data, err := app.Query(ctx, opts)
	if err != nil {
		fmt.Printf("query=%s\nerror=%v\n\n", rawURL, err)
		return
	}

	// 总数同样走 Application：授权后把规约交给仓储统计。
	var total int64
	if err := app.Read(ctx, "list", func(ctx context.Context, spec spec.Spec) error {
		var countErr error
		total, countErr = repo.QueryCount(ctx, opts, spec)
		return countErr
	}); err != nil {
		fmt.Printf("query=%s\nerror=%v\n\n", rawURL, err)
		return
	}

	// 输出保持朴素，目的是让人能直接对照 URL 和结果。
	fmt.Printf("query=%s\n", rawURL)
	fmt.Printf("matched=%d total=%d\n", len(data), total)
	for _, item := range data {
		fmt.Printf("- id=%d name=%s active=%t score=%d created_at=%s\n",
			item.ID, item.Name, item.Active, item.Score, item.CreatedAt.Format("2006-01-02T15:04:05Z07:00"))
	}
	fmt.Println()
}
