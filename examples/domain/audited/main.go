package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"gochen-runtime/api/rest"
	appaudited "gochen/app/audited"
	appcrud "gochen/app/crud"
	"gochen/auth"
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

// main 演示审计型应用、Descriptor 驱动的标准路由与业务动作。
func main() {
	log.Println("=== Audited Demo ===")
	repo, repositoryErr := testkit.NewAuditedMemoryRepository[*Article, int64](testkit.NewInt64Sequence(1))
	if repositoryErr != nil {
		panic(repositoryErr)
	}
	app, err := appaudited.NewApplication[*Article, int64](repo, repo, appaudited.Options[*Article, int64]{
		Options: appcrud.Options[*Article, int64]{
			Resource:   "article",
			Authorizer: auth.Allow(),
			Actions: []auth.Action{
				{Name: "publish", Kind: auth.KindUpdate, Label: "发布文章"},
			},
		},
	})
	if err != nil {
		log.Fatalf("failed to build audited application: %v", err)
	}

	router := testkit.NewRecordingRouter()
	operatorExtractor := func(c httpx.IContext) (string, bool) {
		operator := strings.TrimSpace(c.Header("X-Operator"))
		if operator == "" {
			operator = "system"
		}
		return operator, true
	}
	if err := rest.Mount[*Article, int64](
		router, app,
		rest.WithBasePath[*Article, int64]("/api/articles"),
		rest.WithOperatorExtractor[*Article, int64](operatorExtractor),
	); err != nil {
		log.Fatalf("failed to mount audited routes: %v", err)
	}
	if err := rest.Action[*Article, int64](
		router, app, http.MethodPost, "/api/articles/:id/publish", "publish",
		func(ctx context.Context, c httpx.IContext, app rest.Application[*Article, int64]) error {
			id, err := httpx.ParseInt64Param(c, "id")
			if err != nil {
				return err
			}
			_, err = app.Execute(ctx, "publish", id, func(_ context.Context, article *Article) error {
				if article.Status == "published" {
					return nil
				}
				now := time.Now()
				article.Status = "published"
				article.PublishedAt = &now
				return nil
			}, appaudited.WithReason("manual publish"))
			return err
		},
		rest.WithOperatorExtractor[*Article, int64](operatorExtractor),
	); err != nil {
		log.Fatalf("failed to register business action: %v", err)
	}
	router.PrintRoutes()

	ctx, err := contextx.WithOperator(context.Background(), "alice")
	if err != nil {
		log.Fatal(err)
	}
	if err := app.Create(ctx, &Article{Title: "Hello", Content: "world"}); err != nil {
		log.Fatalf("failed to create article: %v", err)
	}
	published, err := app.Execute(ctx, "publish", 1, func(_ context.Context, article *Article) error {
		now := time.Now()
		article.Status = "published"
		article.PublishedAt = &now
		return nil
	})
	if err != nil {
		log.Fatalf("failed to publish: %v", err)
	}
	log.Printf("published: status=%s", published.Status)

	trail, err := app.AuditTrail(ctx, 1, 0, 20)
	if err != nil {
		log.Fatalf("failed to read audit trail: %v", err)
	}
	log.Printf("audit trail: %d record(s)", len(trail))

	if err := app.Delete(ctx, 1); err != nil {
		log.Fatalf("failed to soft-delete: %v", err)
	}
	deleted, err := app.ListDeleted(ctx, 0, 10)
	if err != nil {
		log.Fatalf("failed to list deleted: %v", err)
	}
	log.Printf("soft deleted: %d record(s) in recycle bin", len(deleted))
}
