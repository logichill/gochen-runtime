package config

import "sync"

// ConfigWarning 表示 tolerant getter 的告警信息。
//
// 说明：
// - 当 key 存在但无法转换为期望类型时，会回退使用 default，并记录一条 warning；
// - 对敏感 key 会对 Value 做脱敏（redacted），避免泄露敏感信息。
type ConfigWarning struct {
	Key      string `json:"key"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
	Value    any    `json:"value"`
	Message  string `json:"message"`
}

const defaultWarningBufferSize = 256

// Provider 聚合多个配置源，并提供带宽松/严格两种模式的类型化读取能力。
type Provider struct {
	sources  []IConfigSource
	settings map[string]any
	entries  []configEntry

	warnMu    sync.Mutex
	warnings  []ConfigWarning
	maxWarns  int
	onWarning func(ConfigWarning)
}

// ProviderOption 配置提供者选项。
type ProviderOption func(*Provider)

// NewProvider 创建 Provider，并在返回前加载所有配置源。
func NewProvider(opts ...ProviderOption) (*Provider, error) {
	p := &Provider{
		sources:  make([]IConfigSource, 0),
		settings: make(map[string]any),
		entries:  make([]configEntry, 0),
		maxWarns: defaultWarningBufferSize,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}

	if err := p.load(); err != nil {
		return nil, err
	}

	return p, nil
}

// 接口断言。
var _ IConfigProvider = (*Provider)(nil)
