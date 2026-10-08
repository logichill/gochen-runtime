package main

import (
	"context"
	"log"
	"time"

	"gochen-runtime/api/rest"
	"gochen/app/crud"
	"gochen/app/query"
	"gochen/httpx"
	"gochen/testkit"
)

// User 简单实体（普通 CRUD）
type User struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Version uint64 `json:"version"`
	Active  bool   `json:"active"`
	// 扩展示例字段
	ActivatedAt *time.Time `json:"activated_at,omitempty"`
}

// GetID 返回用户实体 ID。
func (u *User) GetID() int64 { return u.ID }

// GetVersion 返回乐观锁版本号。
func (u *User) GetVersion() uint64 { return u.Version }

// GetCreatedAt 在该示例中返回零值。
func (u *User) GetCreatedAt() int64 { return 0 }

// GetUpdatedAt 在该示例中返回零值。
func (u *User) GetUpdatedAt() int64 { return 0 }

// SetUpdatedAt 在该简化示例中保留为空实现。
func (u *User) SetUpdatedAt(_ int64) {}

// IsDeleted 在该示例里始终返回 false。
func (u *User) IsDeleted() bool { return false }

// MarkAsDeleted 在该示例里保留为空实现。
func (u *User) MarkAsDeleted() {}

// Restore 在该示例里保留为空实现。
func (u *User) Restore() {}

// GetDeletedAt 在该示例里始终返回 nil。
func (u *User) GetDeletedAt() *int64 { return nil }

// Validate 在该示例里不额外施加校验。
func (u *User) Validate() error { return nil }

// SetID 回填仓储生成的实体 ID。
func (u *User) SetID(id int64) { u.ID = id }

// GetEntityType 返回示例实体类型名。
func (u *User) GetEntityType() string { return "User" }

// 扩展仓储：增加激活方法
type ExtUserRepo struct {
	*testkit.MemoryRepository[*User, int64]
}

// Activate 把指定用户标记为已激活。
func (r *ExtUserRepo) Activate(ctx context.Context, id int64) error {
	u, err := r.Get(ctx, id)
	if err != nil {
		return err
	}
	if u.Active {
		return nil
	}
	now := time.Now()
	u.Active = true
	u.ActivatedAt = &now
	u.SetUpdatedAt(now.Unix())
	return r.Update(ctx, u)
}

// 扩展服务：封装业务方法
type UserServiceExt struct {
	crud.IApplication[*User, int64]
	repo *ExtUserRepo
}

// NewUserServiceExt 创建带扩展业务方法的用户服务包装器。
func NewUserServiceExt(repo *ExtUserRepo, base crud.IApplication[*User, int64]) *UserServiceExt {
	return &UserServiceExt{IApplication: base, repo: repo}
}

// Activate 复用扩展仓储完成激活业务。
func (s *UserServiceExt) Activate(ctx context.Context, id int64) error {
	return s.repo.Activate(ctx, id)
}

// main 演示基础 CRUD 应用服务与 REST 路由的组合方式。
func main() {
	log.Println("=== Basic CRUD Example ===")

	// 1) 依赖（内存模拟）
	baseRepo := testkit.NewMemoryRepository[*User, int64](testkit.NewInt64Sequence(1))
	userRepo := &ExtUserRepo{MemoryRepository: baseRepo}
	router := testkit.NewRecordingRouter()

	// 2) 服务
	baseSvc, err := crud.NewApplication(userRepo, nil)
	if err != nil {
		log.Fatalf("failed to create application service: %v", err)
	}
	svc := NewUserServiceExt(userRepo, baseSvc)

	// 3) 注册 RESTful API
	if err := rest.Register(router, svc,
		func(rb *rest.ApiBuilder[*User, int64]) {
			rb.Route(func(cfg *rest.RouteConfig[int64]) {
				cfg.Routing.EnableBatch = false
			})
		},
	); err != nil {
		log.Fatalf("failed to register API: %v", err)
	}

	// 3.1 注册一个扩展业务方法：激活用户（示例）
	router.POST("/users/:id/activate", func(ctx httpx.IContext) error {
		id, err := httpx.ParseInt64Param(ctx, "id")
		if err != nil {
			return err
		}
		return svc.Activate(ctx.RequestContext(), id)
	})
	router.PrintRoutes()

	// 4) 演示创建/查询/分页
	if err := svc.Create(context.Background(), &User{Name: "张三", Email: "zhangsan@example.com"}); err != nil {
		log.Fatalf("failed to create user: %v", err)
	}
	got, err := svc.Get(context.Background(), 1)
	if err != nil {
		log.Fatalf("failed to get user: %v", err)
	}
	log.Printf("got user: %+v", got)
	page, err := svc.ListPage(context.Background(), &query.PaginationOptions{Page: 1, Size: 10})
	if err != nil {
		log.Fatalf("failed to list page: %v", err)
	}
	log.Printf("pagination: total=%d, items=%d", page.Total, len(page.Data))
}
