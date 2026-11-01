# di：依赖注入契约与默认容器

`gochen-runtime/di` 定义依赖注入的公共契约；默认反射容器位于 `gochen-runtime/di/basic`。Host 与模块装配层通过这组契约完成 provider 注册、构造器解析和启动期诊断。

## 1. 功能定义

| 契约/入口 | 职责 |
| --- | --- |
| `di.IRegistry` | 按类型注册 singleton、transient 或实例 |
| `di.IConstructorRegistry` | 按构造函数返回值自动注册服务 |
| `di.IResolver` | 按类型解析、检查注册 |
| `di.IIntrospector` | 读取已注册类型快照，不实例化对象 |
| `di.IInvoker` | 按函数参数类型解析并调用 |
| `di.IContainer` | 组合以上能力并提供 `Clear` |
| `di.RegisterSingleton[T]` / `RegisterTransient[T]` / `RegisterInstance[T]` | 泛型注册 helper |
| `di.Resolve[T]` | 泛型解析 helper |
| `di.NewFactory` / `NewInstance` / `NewConstructor` / `NewInvocation` | 构造容器调用描述 |
| `di.TypeKey` | 生成稳定的服务类型 key |
| `di.Diagnose` | 读取容器诊断快照与循环依赖 |

## 2. 快速使用

```go
import (
    "log"

    "gochen-runtime/di"
    "gochen-runtime/di/basic"
)

type Database struct{}
type UserService struct {
    db *Database
}

func NewDatabase() *Database { return &Database{} }
func NewUserService(db *Database) *UserService { return &UserService{db: db} }

func main() {
    container := basic.New()

    if err := di.RegisterSingleton[*Database](container, NewDatabase); err != nil {
        log.Fatal(err)
    }
    if err := di.RegisterSingleton[*UserService](container, NewUserService); err != nil {
        log.Fatal(err)
    }

    svc, err := di.Resolve[*UserService](container)
    if err != nil {
        log.Fatal(err)
    }
    _ = svc
}
```

注册规则：

- singleton 工厂只执行一次；transient 每次解析都会执行。
- 工厂返回 `(T)` 或 `(T, error)`，参数由容器按类型注入。
- 构造函数有多个非 error 返回值时，`RegisterConstructor` 共享一次构造结果并分别注册各输出类型。
- 同一服务类型只能注册一次；重复注册返回 `Conflict`。

## 3. 在 Host 启动链路中的位置

```text
Host.Prepare
  ├─ 登记框架 registry 实例
  ├─ 构建模块
  └─ Module.Init
       ├─ 注册模块 provider
       ├─ 按角色保存路由/事件/投影/组件注册项
       └─ 执行 OnInit

Host.StartBackground
  ├─ di.Diagnose：循环依赖 fail-fast
  ├─ RegisterRoutes：按类型解析路由注册器
  └─ Module.Start：解析事件处理器、投影、运行期组件
```

模块作者通过 `ModuleInitOptions.Registry` 注册 provider，通过 `ModuleInitOptions.Resolver` 解析依赖。框架装配层需要完整容器能力时使用 `module.ResolveModuleContainer(opts)`。

## 4. 依赖关系

- `di` 仅依赖 Core 的 `gochen/errors`。
- `di/basic` 实现 `di.IContainer`，负责反射构造、并发解析、循环依赖检测与诊断。
- `host`、`host/module/assembly`、`host/internal/bootstrap` 使用 `di` 完成框架装配。
- 业务运行期通过构造函数参数接收依赖；容器只服务组合根、Host 和框架装配阶段。
