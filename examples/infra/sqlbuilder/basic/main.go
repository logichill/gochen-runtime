package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"gochen-runtime/db/sql/sqlbuilder"
	"gochen-runtime/db/sql/stdsql"
	"gochen/db"
	_ "modernc.org/sqlite"
)

// demoUser 是 SQL Builder 示例里使用的简单查询模型。
type demoUser struct {
	ID   int64
	Name string
	Age  int
}

// main 演示 SQL Builder 的插入、查询和自增更新能力。
func main() {
	ctx := context.Background()

	// 1. 创建基础数据库（使用 SQLite 内存数据库）
	db, err := stdsql.New(db.DBConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		log.Fatalf("failed to create database: %v", err)
	}
	defer func() { _ = db.Close() }()

	// 2. 初始化表结构
	if err := initSchema(ctx, db); err != nil {
		log.Fatalf("failed to init schema: %v", err)
	}

	// 3. 构造 ISQL
	sql, err := sqlbuilder.New(db)
	if err != nil {
		log.Fatalf("failed to create sql builder: %v", err)
	}

	// 4. 插入示例数据
	if err := seedUsers(ctx, sql); err != nil {
		log.Fatalf("failed to seed data: %v", err)
	}

	// 5. 查询所有用户数量
	total, err := countUsers(ctx, sql)
	if err != nil {
		log.Fatalf("failed to count users: %v", err)
	}
	fmt.Printf("总用户数: %d\n", total)

	// 6. 查询 18 岁以上用户，按年龄降序
	users, err := listAdults(ctx, sql, 18)
	if err != nil {
		log.Fatalf("failed to list adults: %v", err)
	}
	fmt.Println("18 岁及以上用户:")
	for _, u := range users {
		fmt.Printf("  - ID=%d, Name=%s, Age=%d\n", u.ID, u.Name, u.Age)
	}

	// 7. 演示使用 SetIncrement 更新（年龄 +1）
	if err := incrementAge(ctx, sql, users[0].ID); err != nil {
		log.Fatalf("failed to update user age: %v", err)
	}
	fmt.Println("已将第一个用户年龄 +1")

	// 8. 重新查询并打印
	updated, err := listAdults(ctx, sql, 18)
	if err != nil {
		log.Fatalf("failed to re-query adults: %v", err)
	}
	fmt.Println("更新后成人用户:")
	for _, u := range updated {
		fmt.Printf("  - ID=%d, Name=%s, Age=%d\n", u.ID, u.Name, u.Age)
	}
}

// initSchema 初始化示例表结构。
func initSchema(ctx context.Context, db db.IDatabase) error {
	ddl := `
CREATE TABLE IF NOT EXISTS demo_users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	age INTEGER NOT NULL,
	created_at DATETIME NOT NULL
);`
	_, err := db.Exec(ctx, ddl)
	return err
}

// seedUsers 向示例表写入一批初始用户数据。
func seedUsers(ctx context.Context, sql sqlbuilder.ISQL) error {
	now := time.Now()
	users := []demoUser{
		{Name: "Alice", Age: 20},
		{Name: "Bob", Age: 17},
		{Name: "Charlie", Age: 30},
	}

	for _, u := range users {
		if _, err := sql.InsertInto("demo_users").
			Columns("name", "age", "created_at").
			Values(u.Name, u.Age, now).
			Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// countUsers 返回示例表中的用户总数。
func countUsers(ctx context.Context, sql sqlbuilder.ISQL) (int64, error) {
	q, args, err := sql.SelectExprUnsafe("COUNT(1)").
		From("demo_users").
		Build()
	if err != nil {
		return 0, err
	}

	db := getDBFromSql(sql)
	row := db.QueryRow(ctx, q, args...)
	var total int64
	if err := row.Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

// listAdults 查询年龄大于等于给定值的用户列表。
func listAdults(ctx context.Context, sql sqlbuilder.ISQL, minAge int) ([]demoUser, error) {
	q, args, err := sql.Select("id", "name", "age").
		From("demo_users").
		Where("age >= ?", minAge).
		OrderBy(sqlbuilder.OrderDesc("age")).
		Build()
	if err != nil {
		return nil, err
	}

	db := getDBFromSql(sql)
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var users []demoUser
	for rows.Next() {
		var u demoUser
		if err := rows.Scan(&u.ID, &u.Name, &u.Age); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// incrementAge 演示通过 SetIncrement 对年龄字段做原地自增。
func incrementAge(ctx context.Context, sql sqlbuilder.ISQL, userID int64) error {
	// 使用 SetIncrement 演示列自增更新
	_, err := sql.Update("demo_users").
		SetIncrement("age", 1).
		Where("id = ?", userID).
		Exec(ctx)
	return err
}

// getDBFromSql 从 SQL Builder 实现中提取底层数据库对象。
func getDBFromSql(sql sqlbuilder.ISQL) db.IDatabase {
	type IHasDB interface {
		GetDB() db.IDatabase
	}
	if h, ok := sql.(IHasDB); ok {
		return h.GetDB()
	}
	// 退化处理：直接 panic，示例环境足够
	panic("ISQL implementation does not expose GetDB()")
}
