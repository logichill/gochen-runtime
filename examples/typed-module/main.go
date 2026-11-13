// typed-module 展示 Repository → Application → Routes → Module → Host 的完整接线。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"gochen-runtime/api/rest"
	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/orm/repo"
	"gochen-runtime/db/sql/stdsql"
	"gochen-runtime/host"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/quick"
	"gochen/app/crud"
	"gochen/auth"
	"gochen/db"
	"gochen/db/orm"
	domaincrud "gochen/domain/crud"
	"gochen/httpx"

	_ "modernc.org/sqlite"
)

const noteResource = "note"

// Note 只选择 ID、版本和业务字段，不强加软删除或审计生命周期。
type Note struct {
	domaincrud.Entity[string]
	Title string `json:"title"`
}

func newNotes(database orm.IOrm) module.ModuleCtor {
	return func() (module.IModule, error) {
		repository, err := repo.NewRepo[*Note, string](database, "notes", repo.WithResourceKind[*Note, string](noteResource))
		if err != nil {
			return nil, err
		}
		application, err := quick.DeclaredApp(repository, crud.Options[*Note, string]{DeleteMode: crud.DeletePurge, Resource: noteResource}, nil,
			auth.CompileOptions{Checker: authz.NewActionChecker()})
		if err != nil {
			return nil, err
		}
		return host.Module("notes").Name("笔记").
			Binding(rest.Resource[*Note, string](application)).
			Build()
	}
}

// demoIdentity 只供回环地址示例使用；真实服务必须替换为可信认证入口。
// 用户头不能在生产服务中被直接当作身份或权限证明。
func demoIdentity(c httpx.IContext, next func() error) error {
	role := c.Header("X-Demo-Role")
	var permissions []string
	switch role {
	case "reader":
		permissions = []string{"note:api:read", "note:api:list"}
	case "editor":
		permissions = []string{"note:api:*"}
	default:
		return next() // 不注入身份，应用授权会拒绝数据访问。
	}
	ctx, err := authz.WithPrincipal(c.RequestContext(), authz.Principal{SubjectID: role, Permissions: permissions})
	if err != nil {
		return err
	}
	c.SetContext(c.RequestContext().WithContext(ctx))
	return next()
}

func openNotes(ctx context.Context) (db.IDatabase, orm.IOrm, error) {
	database, err := stdsql.NewWithContext(ctx, db.DBConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
	if err != nil {
		return nil, nil, err
	}
	// 仅在这个独立内存示例中创建表；不迁移任何现存数据库。
	if _, err = database.Exec(ctx, `CREATE TABLE notes (id TEXT PRIMARY KEY, version INTEGER NOT NULL, title TEXT NOT NULL)`); err != nil {
		return database, nil, err
	}
	engine, err := lite.New(database)
	return database, engine, err
}

func main() {
	port := flag.Int("port", 8080, "回环地址 HTTP 端口")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *port); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, port int) (err error) {
	database, engine, err := openNotes(ctx)
	if database != nil {
		defer func() { err = errors.Join(err, database.Close()) }()
	}
	if err != nil {
		return err
	}
	log.Printf("仅供本机演示：http://127.0.0.1:%d/api/v1/notes", port)
	// 连接由组合根持有，Host 退出后才关闭；模块只登记同一个应用实例。
	return quick.New(config.WithName("typed-notes"), config.WithAddress("127.0.0.1"), config.WithPort(port),
		config.WithEntryValidator(authz.NewEntryRegistry())).
		Use(demoIdentity).Modules(newNotes(engine)).Run(ctx)
}
