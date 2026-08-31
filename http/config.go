package http

import (
	"crypto/tls"
	"time"

	"gochen/httpx"
)

// WebConfig HTTP 服务基础配置。
type WebConfig struct {
	Host string `json:"host" yaml:"host" default:"0.0.0.0"`
	Port int    `json:"port" yaml:"port" default:"8080" validate:"min=1024,max=65535"`
	Mode string `json:"mode" yaml:"mode" default:"release" validate:"required,oneof=debug release test"`
	// EnableRequestLog 控制适配层是否启用框架自带访问日志。
	//
	// 说明：
	// - nil 表示由具体适配层按既有默认行为决定；
	// - 显式 false 可避免与业务自定义 access log 重复输出。
	EnableRequestLog *bool `json:"enable_request_log,omitempty" yaml:"enable_request_log,omitempty"`

	// SecurityLayer 表示该 HTTP 服务默认承载的“安全分层”语义（API/Web）。
	//
	// 说明：
	// - 该字段仅用于“语义标签 + 防误用约束”（例如 API 默认拒绝 Session 语义），不直接实现鉴权/CSRF 等能力；
	// - 对于需要更精细控制的场景（同一进程同时承载 API 与 Web），建议通过路由 group 分别挂载
	//   `httpx/middleware.SecurityLayer` 来覆盖该默认值。
	SecurityLayer httpx.SecurityLayer `json:"security_layer" yaml:"security_layer"`
	// AllowSession 控制是否允许 Session 语义（gochencontext.SessionID）。
	//
	// 说明：
	// - nil 表示按 SecurityLayer 取默认值：API -> false（默认拒绝），Web -> true；
	// - 该字段不提供 Session 的创建/验证/持久化能力，仅用于减少误用。
	AllowSession *bool `json:"allow_session" yaml:"allow_session"`
	// ReadHeaderTimeout 读取请求头超时（用于抵抗 slowloris/慢头攻击）。
	//
	// 说明：
	// - 安全默认：如果未设置，将使用 DefaultReadHeaderTimeout；
	// - 若需禁用（不建议），可显式设置为负值，normalize 时会归零。
	ReadHeaderTimeout time.Duration `json:"read_header_timeout" yaml:"read_header_timeout"`
	ReadTimeout       time.Duration `json:"read_timeout" yaml:"read_timeout" default:"30s"`
	WriteTimeout      time.Duration `json:"write_timeout" yaml:"write_timeout" default:"30s"`
	IdleTimeout       time.Duration `json:"idle_timeout" yaml:"idle_timeout" default:"60s"`
	// MaxHeaderBytes 最大请求头大小（用于防止超大 header 造成资源消耗放大）。
	//
	// 说明：
	// - 安全默认：如果未设置，将使用 DefaultMaxHeaderBytes。
	MaxHeaderBytes int `json:"max_header_bytes" yaml:"max_header_bytes"`
	// TrustedProxies 可信代理列表（IP 或 CIDR），用于决定是否信任 X-Forwarded-For/X-Real-IP 等代理头。
	//
	// 安全默认：当该字段为空时，不信任任何代理头，ClientIP() 仅返回 RemoteAddr。
	TrustedProxies []string `json:"trusted_proxies" yaml:"trusted_proxies"`

	// TLS
	TLSEnabled bool   `json:"tls_enabled" yaml:"tls_enabled"`
	CertFile   string `json:"cert_file" yaml:"cert_file"`
	KeyFile    string `json:"key_file" yaml:"key_file"`
	// TLSMinVersion TLS 最低版本。
	//
	// 说明：
	// - 仅在 TLSEnabled=true 时生效；
	// - 安全默认：未设置时将使用 TLS 1.2；
	// - 允许值（大小写不敏感）："1.2"/"1.3"（也支持 "tls1.2"/"tls1.3"）。
	TLSMinVersion string `json:"tls_min_version" yaml:"tls_min_version"`
	// TLSConfig 允许以编程方式注入 tls.Config（用于 in-memory certs/GetCertificate 等）。
	//
	// 注意：
	// - 该字段不会被 json/yaml 反序列化；如需配置文件驱动，请使用 TLSMinVersion + CertFile/KeyFile。
	TLSConfig *tls.Config `json:"-" yaml:"-"`

	// CORS
	// 安全默认：CORSEnabled 默认关闭，避免未显式配置时意外开放跨域。
	// 若需启用 CORS，请同时设置 CORSEnabled=true 并在 CORSAllowOrigins 中显式列出允许的 Origin。
	// WebConfig 仅承载配置；调用方仍需显式注册 middleware.CORSFromWebConfig(cfg) 或等价 CORS 中间件。
	CORSEnabled          bool     `json:"cors_enabled" yaml:"cors_enabled" default:"false"`
	CORSAllowOrigins     []string `json:"cors_allow_origins" yaml:"cors_allow_origins"`
	CORSAllowMethods     []string `json:"cors_allow_methods" yaml:"cors_allow_methods" default:"GET,POST,PUT,DELETE,PATCH,OPTIONS"`
	CORSAllowHeaders     []string `json:"cors_allow_headers" yaml:"cors_allow_headers" default:"Content-Type,Authorization,Origin,X-CSRF-Token"`
	CORSAllowCredentials bool     `json:"cors_allow_credentials" yaml:"cors_allow_credentials" default:"false"`
}

// 默认超时配置（适用于未显式配置的开发/简单场景）：
const (
	// DefaultReadHeaderTimeout 定义相关常量。
	DefaultReadHeaderTimeout = 5 * time.Second
	// DefaultMaxHeaderBytes 定义相关常量。
	DefaultMaxHeaderBytes = 1 << 20 // 1MB（与 net/http 默认一致）
	// DefaultReadTimeout 定义相关常量。
	DefaultReadTimeout = 15 * time.Second
	// DefaultWriteTimeout 定义相关常量。
	DefaultWriteTimeout = 15 * time.Second
	// DefaultIdleTimeout 定义相关常量。
	DefaultIdleTimeout = 60 * time.Second
	// DefaultWebHost HTTP 默认监听地址。
	DefaultWebHost = "0.0.0.0"
	// DefaultWebPort HTTP 默认监听端口（与 HostConfig 默认端口保持一致）。
	DefaultWebPort = 8080
	// DefaultWebMode HTTP 默认运行模式。
	DefaultWebMode = "release"
)

// DefaultWebConfig 返回带默认值的 WebConfig，作为 NewServer(nil) 与配置加载的单一默认来源。
func DefaultWebConfig() *WebConfig {
	return &WebConfig{
		Host: DefaultWebHost,
		Port: DefaultWebPort,
		Mode: DefaultWebMode,
	}
}
