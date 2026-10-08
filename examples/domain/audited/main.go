package main

import (
	"context"
	"log"
	"strings"
	"time"

	"gochen-runtime/api/rest"
	appaudited "gochen/app/audited"
	"gochen/contextx"
	daudited "gochen/domain/audited"
	"gochen/httpx"
	"gochen/testkit"
)

// Article 演示同时具备审计字段和软删除能力的实体。
type Article struct {
	daudited.AuditedEntity[int64]
	Title       string     `json:"title"`
	Content     string     `json:"content"`
	Status      string     `json:"status"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

// Validate 在该示例里不额外施加业务校验。
func (a *Article) Validate() error { return nil }

// 使用 testkit.NewAuditedMemoryRepository 提供的通用审计型内存仓储。

// ArticleRepo 在通用审计仓储之上补充文章发布能力。
type ArticleRepo struct {
	*testkit.AuditedMemoryRepository[*Article, int64]
}

// Publish 把文章标记为已发布并补齐审计字段。
func (r *ArticleRepo) Publish(ctx context.Context, id int64, by string) (*Article, error) {
	art, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if art.Status == "published" {
		return nil, nil
	}
	now := time.Now()
	art.Status = "published"
	art.PublishedAt = &now
	// 更新审计信息并持久化
	art.SetUpdatedInfo(by, now)
	return art, nil
}

// ArticleService 演示如何在审计应用服务之上扩展领域方法。
type ArticleService struct {
	appaudited.IApplication[*Article, int64]
	repo *ArticleRepo
}

// Publish 通过 audited application 统一完成发布后的持久化与审计落库。
func (s *ArticleService) Publish(ctx context.Context, id int64, by string) error {
	art, err := s.repo.Publish(ctx, id, by)
	if err != nil {
		return err
	}
	if art == nil {
		return nil
	}
	// 走 audited application 的 Update：写实体 + 写审计记录（同事务）
	ctx, err = contextx.WithOperator(ctx, by)
	if err != nil {
		return err
	}
	return s.Update(ctx, art)
}

// main 演示审计型应用服务与 REST 构建器的组合方式。
func main() {
	log.Println("=== Audited Demo ===")
	baseRepo := testkit.NewAuditedMemoryRepository[*Article, int64](testkit.NewInt64Sequence(1))
	repo := &ArticleRepo{AuditedMemoryRepository: baseRepo}
	// 使用 audited application 作为 CRUD+审计的组合入口；同时演示如何在 audited service 之上扩展领域能力。
	app, err := appaudited.NewApplication(repo, repo, nil)
	if err != nil {
		log.Fatalf("failed to build audited application: %v", err)
	}
	svc := &ArticleService{IApplication: app, repo: repo}
	router := testkit.NewRecordingRouter()
	// 使用统一 REST 构建器注册标准 + audited 扩展接口（audited entity 默认启用）
	if err := rest.Register(
		router,
		app,
		func(b *rest.ApiBuilder[*Article, int64]) {
			b.Route(func(rc *rest.RouteConfig[int64]) {
				rc.Routing.BasePath = "/api/articles"
				rc.Audit.OperatorExtractor = func(c httpx.IContext) (string, bool) {
					// Demo：没有接入鉴权时，允许通过 header 传入；缺省为 "system"
					op := strings.TrimSpace(c.Header("X-Operator"))
					if op == "" {
						op = "system"
					}
					return op, true
				}
			})
		},
	); err != nil {
		log.Fatalf("failed to register audited routes: %v", err)
	}
	// 扩展业务方法：发布文章（示例）
	router.POST("/api/articles/:id/publish", func(ctx httpx.IContext) error {
		id, err := httpx.ParseInt64Param(ctx, "id")
		if err != nil {
			return err
		}
		operator := strings.TrimSpace(ctx.Header("X-Operator"))
		if operator == "" {
			operator = "system"
		}
		return svc.Publish(ctx.RequestContext(), id, operator)
	})
	router.PrintRoutes()

	// 此示例仅演示路由注册，业务调用留给实际系统验证
}
