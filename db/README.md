# db：物理数据库适配与数据访问驱动

`gochen-runtime/db` 是 Runtime 模块提供的物理数据访问驱动集合，为 Core 的 `domain/crud`、`app/crud`、`app/audited` 与 `db/orm` 接口提供具体的 SQL 持久化、ORM 仓储、SQL Builder、架构迁移与 Schema 差异分析支持。

---

## 1. 子包结构与职责

| 子包 | 职责 | 核心能力 |
|---|---|---|
| **`db/orm/repo`** | 通用 ORM 仓储实现 | 实现 `crud.IRepository` 与 `audited.IAuditedRepository`，支持条件查询、分页、事务、软删除、审计与乐观锁 |
| **`db/orm/lite`** | 轻量 SQL ORM 驱动 | 基于纯 `database/sql` 的轻量 ORM 引擎，满足 `gochen/db/orm.IOrm` 抽象 |
| **`db/sql/sqlbuilder`** | 模块化 SQL Builder | 参数化安全构造 Select / Insert / Update / Delete / Upsert 语句，适配各数据库方言 |
| **`db/migrate`** | SQL 迁移运行器 | 单文件事务包装、数据库行级排他锁、自动过期锁回收、人工审阅安全守卫 |
| **`db/schema`** | Schema AST 与差异分析 | 数据库元数据反查（Introspect）、Schema Diff 与正反向 DDL 渲染 |

---

## 2. 快速使用

### 2.1 创建 ORM 仓储

```go
import (
    "gochen-runtime/db/orm/lite"
    "gochen-runtime/db/orm/repo"
)

// 1. 初始化 ORM Engine
ormEngine := lite.New(db) // db 为 *sql.DB

// 2. 创建实体 CRUD 仓储
userRepo, err := repo.NewRepository[*User, int64](
    ormEngine,
    repo.WithTableName("users"),
    repo.WithIDColumn("id"),
)
```

### 2.2 运行数据库迁移 (Migration)

```go
import "gochen-runtime/db/migrate"

source := migrate.NewFileSource("migrations") // 或 migrate.NewFS(embedFS, "migrations")
runner := migrate.NewRunner(db, source)

// 升级到最新版本
if err := runner.Up(ctx); err != nil {
    log.Fatalf("migration failed: %v", err)
}
```

---

## 3. 详细指南

- 数据库表结构迁移与规范：`gochen` 仓库 `docs/guides/db-schema-migration-guide.md`
- Core ORM 抽象接口：`gochen` 仓库 `db/orm/README.md`
