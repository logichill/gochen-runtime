package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"gochen-runtime/api/rest"
	"gochen/app/crud"
	"gochen/auth"
	domcrud "gochen/domain/crud"
	"gochen/httpx"
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
// 业务动作（activate）通过 rest.Action 注册，仍由 Application 的 Execute
// 完成授权、事务与钩子；路由层只做解码与编排。
func main() {
	log.Println("=== Basic CRUD Example ===")

	repo := testkit.NewMemoryRepository[*User, int64](testkit.NewInt64Sequence(1))
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
	if err := rest.Action[*User, int64](
		router, app, http.MethodPost, "/users/:id/activate", "activate",
		func(ctx context.Context, c httpx.IContext, app rest.Application[*User, int64]) error {
			id, err := httpx.ParseInt64Param(c, "id")
			if err != nil {
				return err
			}
			_, err = app.Execute(ctx, "activate", id, func(_ context.Context, user *User) error {
				if user.Active {
					return nil
				}
				now := time.Now()
				user.Active = true
				user.ActivatedAt = &now
				return nil
			})
			return err
		},
	); err != nil {
		log.Fatalf("failed to register business action: %v", err)
	}
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
