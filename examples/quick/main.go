package main

import (
	"context"
	"gochen/domain/meta"

	stderrors "errors"
	"fmt"
	"log"
	"net"
	"time"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/quick"
	"gochen/app/crud"
	"gochen/auth"

	_ "modernc.org/sqlite"

	"gochen/contextx"
	"gochen/db"
	"gochen/domain/spec"
	"gochen/errors"
)

// Order 演示租户隔离与数据范围实体
type Order struct {
	auth.TenantEntity[int64]
	OrgID  int64  `json:"org_id" dim:"scope"`
	Title  string `json:"title"`
	Amount int64  `json:"amount"`
}

// scopeResolver 将激活范围映射为 scope 维度条件，并声明所需词汇。
type scopeResolver struct{}

func (scopeResolver) Vocabulary() auth.Vocabulary {
	return auth.Vocabulary{Refs: []meta.Ref{meta.ByDimension("scope")}}
}

func (scopeResolver) ResolveSpec(ctx context.Context, _ auth.Request) (spec.Spec, []auth.Assignment, error) {
	principal, ok := authz.PrincipalFromContext(ctx)
	if !ok || principal.ActiveScopeID <= 0 {
		return spec.Spec{}, nil, errors.NewCode(errors.Unauthorized, "active scope is required")
	}
	return spec.Eq(meta.ByDimension("scope"), principal.ActiveScopeID), nil, nil
}

func (o *Order) Validate() error { return nil }

func main() {
	log.SetFlags(0)

	// 1. 初始化数据库与全局切面列名约定
	database, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		log.Fatalf("failed to create database: %v", err)
	}
	defer func() { _ = database.Close() }()

	// 创建数据表结构（自动匹配 NamingConvention 规范）
	_, err = database.Exec(context.Background(), `CREATE TABLE orders (
		id INTEGER PRIMARY KEY,
		tenant_id TEXT NOT NULL,
		org_id INTEGER NOT NULL,
		title TEXT NOT NULL,
		amount INTEGER NOT NULL,
		version INTEGER NOT NULL DEFAULT 1
	)`)
	if err != nil {
		log.Fatalf("failed to create table: %v", err)
	}

	// 预置初始业务数据（涵盖多租户与多业务范围）
	_, err = database.Exec(context.Background(), `INSERT INTO orders (id, tenant_id, org_id, title, amount, version) VALUES
		(101, 'tenant-apple', 1001, 'MacBook Pro 16"', 27999, 1),
		(102, 'tenant-apple', 1001, 'Studio Display', 11499, 1),
		(103, 'tenant-apple', 1002, 'iPhone 16 Pro', 7999, 1),
		(104, 'tenant-google', 1001, 'Pixel 9 Pro', 6999, 1)
	`)
	if err != nil {
		log.Fatalf("failed to seed initial orders: %v", err)
	}

	ormEngine, err := lite.New(database, lite.WithNamingConvention(db.NamingConvention{
		TenantColumn: "tenant_id",
		ScopeColumn:  "org_id",
	}))
	if err != nil {
		log.Fatalf("failed to create orm engine: %v", err)
	}

	// 2. 极简装配：隔离在 Repo 构造期显式声明，数据范围由授权器下推为规约。
	orderRepo, err := quick.IsolatedRepo[*Order, int64](ormEngine, "orders",
		repo.WithResourceKind[*Order, int64]("order"),
	)
	if err != nil {
		log.Fatalf("failed to create isolated repo: %v", err)
	}

	orderApp, err := quick.DeclaredApp[*Order, int64](orderRepo, crud.Options[*Order, int64]{Resource: "order"},
		[]auth.Rule{{Scope: &auth.ScopeRule{Resolver: scopeResolver{}}}}, auth.CompileOptions{Checker: authz.NewActionChecker()})
	if err != nil {
		log.Fatal(err)
	}

	// 3. 业务查询：租户隔离与 scope 维度条件共同生效。
	userCtx := mustPrincipalContext("tenant-apple", 1001, "order:api:read", "order:api:list")
	orders, err := orderApp.List(userCtx, 0, 10)
	if err != nil {
		log.Fatalf("failed to list orders: %v", err)
	}

	fmt.Printf("获取当前租户 (tenant-apple) 且业务范围 (OrgID=1001) 的订单列表 (共 %d 条):\n", len(orders))
	for _, o := range orders {
		fmt.Printf("  - 订单 #%d: %-18s 金额: ¥%-6d (租户: %s, 业务范围: %d, 版本: %d)\n",
			o.GetID(), o.Title, o.Amount, o.GetTenantID(), o.OrgID, o.GetVersion())
	}

	// 4. 模块声明与微服务生命周期启动；路由由 Descriptor 推导。
	orderModule := quick.Module("order", quick.RESTRegistrar[*Order, int64](orderApp))
	server, err := quick.New(config.WithName("order-service"), config.WithPort(freePort())).Modules(orderModule).Build()
	if err != nil {
		log.Fatalf("failed to create quick server: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := server.Start(ctx); err != nil && !stderrors.Is(err, context.Canceled) && !stderrors.Is(err, context.DeadlineExceeded) {
		log.Fatalf("server run failed: %v", err)
	}
}

// freePort 让内核分配一个空闲端口，避免示例固定占用端口与本机服务相撞。
func freePort() int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("failed to allocate free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		log.Fatalf("failed to release probe listener: %v", err)
	}
	return port
}

func mustPrincipalContext(tenantID string, activeScopeID int64, permissions ...string) context.Context {
	ctx := context.Background()
	var err error
	ctx, err = contextx.WithTenantID(ctx, tenantID)
	if err != nil {
		log.Fatal(err)
	}
	ctx, err = authz.WithPrincipal(ctx, authz.Principal{
		SubjectID:     "1",
		ActiveScopeID: activeScopeID,
		Permissions:   permissions,
	})
	if err != nil {
		log.Fatal(err)
	}
	return ctx
}
