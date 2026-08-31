# di：类型安全依赖注入容器

`gochen-runtime/di` 是 Runtime 模块提供的轻量、类型安全依赖注入（Dependency Injection）容器，主要用于 `gochen-runtime/host` 模块装配与组合根构建。

---

## 1. 核心特性

- **类型安全**：借助 Go 泛型提供编译期友好的 `Resolve[T]` 与 `Provide` 注册；
- **构造函数注入**：自动解析构造函数的参数依赖并按拓扑顺序实例化；
- **循环依赖检测**：在装配与解析期检测依赖成环并给出清晰诊断报告；
- **接口绑定**：支持将具体实现 struct 绑定并暴露为接口服务。

---

## 2. 快速使用

### 2.1 注册 Provider 并解析

```go
package main

import (
    "fmt"
    "log"

    "gochen-runtime/di"
)

type Database struct{}
type UserService struct {
    db *Database
}

func NewDatabase() *Database {
    return &Database{}
}

func NewUserService(db *Database) *UserService {
    return &UserService{db: db}
}

func main() {
    c := di.NewContainer()

    // 1. 注册构造函数
    if err := di.Provide(c, NewDatabase); err != nil {
        log.Fatal(err)
    }
    if err := di.Provide(c, NewUserService); err != nil {
        log.Fatal(err)
    }

    // 2. 类型安全解析
    svc, err := di.Resolve[*UserService](c)
    if err != nil {
        log.Fatalf("resolve failed: %v", err)
    }

    fmt.Printf("UserService initialized with db: %v\n", svc != nil)
}
```

---

## 3. 在 Host 中的应用

在标准的 gochen 模块化应用中，通常不需要手动调用 DI 容器 API，而是通过 `host.Module` 声明式提供：

```go
host.Module("user").
    Provide(
        NewDatabase,
        NewUserRepository,
        NewUserService,
    )
```

Host 启动时会自动利用 DI 容器完成全模块依赖的拓扑排序、实例化与生命周期托管。
