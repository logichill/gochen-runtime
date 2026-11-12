// business-actions 展示同一个受保护审批用例的 HTTP 与命名业务入口。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os/signal"
	"syscall"

	"gochen-runtime/db/orm/lite"
	"gochen-runtime/db/sql/stdsql"
	"gochen-runtime/host/authz"
	"gochen-runtime/host/config"
	"gochen-runtime/host/module"
	"gochen-runtime/quick"
	"gochen/db"
	"gochen/db/orm"

	_ "modernc.org/sqlite"
)

func openOrders(ctx context.Context) (db.IDatabase, orm.IOrm, error) {
	database, err := stdsql.NewWithContext(ctx, db.DBConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1})
	if err != nil {
		return nil, nil, err
	}
	// 数据库随进程创建和释放，不连接、迁移或写入任何生产数据库。
	_, err = database.Exec(ctx, `CREATE TABLE orders (
		id INTEGER PRIMARY KEY,
		version INTEGER NOT NULL,
		department_id INTEGER NOT NULL,
		status TEXT NOT NULL,
		reason TEXT NOT NULL
	)`)
	if err != nil {
		return database, nil, err
	}
	_, err = database.Exec(ctx, `INSERT INTO orders (id, version, department_id, status, reason) VALUES
		(101, 7, 10, 'pending', ''),
		(102, 7, 10, 'pending', ''),
		(103, 7, 20, 'pending', ''),
		(104, 8, 10, 'approved', 'Already approved'),
		(105, 7, 10, 'pending', '')`)
	if err != nil {
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
	database, engine, err := openOrders(ctx)
	if database != nil {
		defer func() { err = errors.Join(err, database.Close()) }()
	}
	if err != nil {
		return err
	}
	approver, orders, err := newOrders(engine)
	if err != nil {
		return err
	}
	// 普通进程内调用，不声称已创建 Job、调度器或持久回执。
	trusted, err := demoContext(ctx, "approver")
	if err != nil {
		return err
	}
	approval, err := approver.Approve(trusted, ApproveOrder{ID: 102, ExpectedVersion: 7, Reason: "Budget confirmed"})
	if err != nil {
		return err
	}
	log.Printf("进程内审批结果：%+v", approval)
	log.Printf("HTTP 审批：POST http://127.0.0.1:%d/api/v1/orders/101/approve", port)
	return quick.New(config.WithName("business-orders"), config.WithAddress("127.0.0.1"), config.WithPort(port),
		config.WithEntryValidator(authz.NewEntryRegistry())).Use(demoIdentity).
		Modules(func() (module.IModule, error) { return orders, nil }).Run(ctx)
}
