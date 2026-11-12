package main

import (
	"context"
	"log"
	"time"

	"gochen-runtime/api/rest"
	"gochen/app/crud"
	"gochen/auth"
	domcrud "gochen/domain/crud"
	"gochen/testkit"
)

// User 业务实体（内嵌通用 CRUD 实体基类，自动具备 ID、Version 及访问器）
type User struct {
	domcrud.Entity[int64]
	Name        string     `json:"name"`
	Email       string     `json:"email"`
	Active      bool       `json:"active"`
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
}

// main 演示基础 CRUD 应用与 Descriptor 驱动的 REST 路由。
//
// 本例展示 Application 的资源操作；命名业务动作的类型化 HTTP 入口见 business-actions。
func main() {
	log.Println("=== Basic CRUD Example ===")

	repo, repositoryErr := testkit.NewMemoryRepository[*User, int64](testkit.NewInt64Sequence(1))
	if repositoryErr != nil {
		panic(repositoryErr)
	}
	app, err := crud.NewApplication[*User, int64](repo, crud.Options[*User, int64]{
		Resource:   "user",
		Authorizer: auth.Allow(),
		Actions: []auth.Action{
			{Name: "activate", Kind: auth.KindUpdate, Label: "激活用户"},
		},
	})
	if err != nil {
		log.Fatalf("failed to create application: %v", err)
	}

	router := testkit.NewRecordingRouter()
	if err := rest.Mount[*User, int64](router, app, rest.WithBasePath[*User, int64]("/users")); err != nil {
		log.Fatalf("failed to mount standard routes: %v", err)
	}
	// 自定义命令的 HTTP 与后台共用绑定见 examples/business-actions。

	router.PrintRoutes()

	ctx := context.Background()
	if err := app.Create(ctx, &User{Name: "张三", Email: "zhangsan@example.com"}); err != nil {
		log.Fatalf("failed to create user: %v", err)
	}
	got, err := app.Get(ctx, 1)
	if err != nil {
		log.Fatalf("failed to get user: %v", err)
	}
	log.Printf("got user: %+v", got)

	activated, err := app.Execute(ctx, "activate", 1, func(_ context.Context, user *User) error {
		user.Active = true
		now := time.Now()
		user.ActivatedAt = &now
		return nil
	})
	if err != nil {
		log.Fatalf("failed to activate user: %v", err)
	}
	log.Printf("activated: active=%v at=%v", activated.Active, activated.ActivatedAt != nil)

	items, err := app.List(ctx, 0, 10)
	if err != nil {
		log.Fatalf("failed to list: %v", err)
	}
	total, err := app.Count(ctx)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("pagination: total=%d, items=%d", total, len(items))
}
