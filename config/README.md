# config：多源配置加载与校验

`gochen-runtime/config` 是 Runtime 模块提供的配置中心组件，支持从 YAML 文件、环境变量、.env 文件与内存默认值等多源加载配置，并提供结构体字段映射、默认值注入与参数校验能力。

---

## 1. 核心特性

- **多源组合**：按优先级合并 YAML 文件、环境变量（`APP_*`）、`.env` 文件与代码内置默认值；
- **单一入口**：统一通过 `provider.Load(key, target, opts...)` 解析配置；
- **标签驱动**：
  - `config:"port"`：字段键名映射；
  - `default:"8080"`：未配置时的零值兜底；
  - `validate:"required,min=1"`：基于结构体标签的参数校验；
  - `env:"CUSTOM_ENV_NAME"`：显式环境变量覆盖；
- **强类型与严格转换**：拒绝模糊转换与非整数浮点截断，防止配置静默降级。

---

## 2. 快速使用

### 2.1 定义配置结构体

```go
type ServerConfig struct {
    Host    string `config:"host" default:"0.0.0.0"`
    Port    int    `config:"port" default:"8080" validate:"required,min=1,max=65535"`
    Timeout int    `config:"timeout_ms" default:"5000"`
}
```

### 2.2 构建 Provider 并加载

```go
import "gochen-runtime/config"

// 1. 创建 Provider，按优先级组合配置源
provider, err := config.New(
    config.WithYAMLFile("config/app.yaml"),
    config.WithEnv("APP"), // 自动匹配 APP_SERVER_PORT 等环境变量
)
if err != nil {
    log.Fatal(err)
}

// 2. 加载并执行验证
var serverCfg ServerConfig
if err := provider.Load("server", &serverCfg, config.WithValidation()); err != nil {
    log.Fatalf("load server config failed: %v", err)
}
```

---

## 3. 加载选项 (`LoadOption`)

- `config.WithValidation()`：开启基于 `validate` tag 的自动校验；
- `config.WithImplicitEnvironment()`：根据字段路径自动探测推导环境变量（如 `server.port` $\to$ `APP_SERVER_PORT`）；
- `config.WithPostBind(fn)`：绑定完成后的自定义后处理钩子。
