package main

import (
	"context"
	"fmt"
	"log"

	"gochen-runtime/host"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen/httpx"
	"gochen/testkit"
)

// 这个示例展示模块化应用的推荐接入路径：
// host.Module 声明显式构造的路由注册器和权限目录，
// host.Run 负责 DI 装配、模块 Init、路由挂载和启动期冲突检查。
var catalogPermissions = authz.NewAPIPermissionSet(
	"catalog",
	authz.PermissionActionRead,
	authz.PermissionActionWrite,
)

type catalogItem struct {
	ID       string
	TenantID string
}

type catalogService struct{}

func NewCatalogService() *catalogService {
	return &catalogService{}
}

type catalogRoutes struct {
	service *catalogService
}

func NewCatalogRoutes(service *catalogService) *catalogRoutes {
	return &catalogRoutes{service: service}
}

func (r *catalogRoutes) RegisterRoutes(group httpx.IRouteGroup) error {
	if r == nil || r.service == nil {
		return fmt.Errorf("catalog routes require service")
	}
	group.GET("/items", func(c httpx.IContext) error {
		return c.JSON(200, httpx.JSONValue(map[string]any{"items": []string{"book", "pen"}}))
	})
	return nil
}

func NewCatalogModule() (host.IModule, error) {
	service := NewCatalogService()
	routes := NewCatalogRoutes(service)
	return host.Module("catalog").
		Name("Catalog").
		RouteRegistrar(routes).
		Extension(authz.Catalog{
			PermissionDefinitions: catalogPermissions.Definitions(),
		}).
		Build()
}

func main() {
	log.SetFlags(0)

	server := testkit.NewRecordingServer()
	registry := authz.NewRegistry()

	if err := host.Run(context.Background(),
		config.WithName("catalog-service"),
		config.WithHTTPServer(server),
		config.WithCatalogRegistrar(authz.NewCatalogRegistrar(registry)),
		config.WithBasePath("/api/v1"),
		config.WithModules(NewCatalogModule),
		config.WithFailFastOnRouteConflicts(true),
	); err != nil {
		log.Fatal(err)
	}

	log.Println("routes:")
	for _, route := range server.RecordedRoutes() {
		log.Printf("  %s %s", route.Method, route.Path)
	}

	log.Println("permissions:")
	for _, permission := range registry.Permissions() {
		log.Printf("  %s", permission)
	}

}
