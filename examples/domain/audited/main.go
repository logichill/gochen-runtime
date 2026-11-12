package main

import (
	"context"
	"log"
	"strings"
	"time"

	"gochen-runtime/api/rest"
	appaudit "gochen/app/audit"
	appcrud "gochen/app/crud"
	appdeletion "gochen/app/deletion"
	"gochen/auth"
	"gochen/contextx"
	"gochen/domain/crud"
	"gochen/httpx"
	"gochen/testkit"
)

// Article 演示同时具备审计字段和软删除能力的实体。
type Article struct {
	crud.TrackedEntity[int64]
	Title       string     `json:"title"`
	Content     string     `json:"content"`
	Status      string     `json:"status"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
}

// Validate 在该示例里不额外施加业务校验。
func (a *Article) Validate() error { return nil }

// main 演示CRUD、审计读取和删除能力组合、Descriptor 驱动的标准路由与业务动作。
func main() {
	log.Println("=== Independent Capabilities Demo ===")
	repo, repositoryErr := testkit.NewAuditedMemoryRepository[*Article, int64](testkit.NewInt64Sequence(1))
	if repositoryErr != nil {
		panic(repositoryErr)
	}
	options := appcrud.Options[*Article, int64]{Resource: "article", Authorizer: auth.Allow(), Audit: repo,
		Actions: []auth.Action{{Name: "publish", Kind: auth.KindUpdate, Label: "发布文章"}},
	}
	app, err := appcrud.NewApplication[*Article, int64](repo, options)
	if err != nil {
		log.Fatal(err)
	}
	trash, err := appdeletion.NewApplication[*Article, int64](repo, appdeletion.Options[*Article, int64]{Options: appcrud.Options[*Article, int64]{Resource: "article", Authorizer: auth.Allow(), Audit: repo}})
	if err != nil {
		log.Fatal(err)
	}
	history, err := appaudit.NewReader[int64](repo, repo, appaudit.Options{Resource: "article", Authorizer: auth.Allow()})
	if err != nil {
		log.Fatal(err)
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
		rest.WithDeletion[*Article, int64](trash),
		rest.WithAudit[*Article, int64](history),
		rest.WithBasePath[*Article, int64]("/api/articles"),
		rest.WithOperatorExtractor[*Article, int64](operatorExtractor),
	); err != nil {
		log.Fatalf("failed to mount audited routes: %v", err)
	}
	// 自定义命令的 HTTP 与后台共用绑定见 examples/business-actions。

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

	trail, err := history.AuditTrail(ctx, 1, 0, 20)
	if err != nil {
		log.Fatalf("failed to read audit trail: %v", err)
	}
	log.Printf("audit trail: %d record(s)", len(trail))

	if err := trash.Delete(ctx, 1); err != nil {
		log.Fatalf("failed to soft-delete: %v", err)
	}
	deleted, err := trash.ListDeleted(ctx, 0, 10)
	if err != nil {
		log.Fatalf("failed to list deleted: %v", err)
	}
	log.Printf("soft deleted: %d record(s) in recycle bin", len(deleted))
}
