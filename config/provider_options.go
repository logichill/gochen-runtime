package config

// WithSource 追加一个原始配置源。
func WithSource(source IConfigSource) ProviderOption {
	return func(p *Provider) {
		p.sources = append(p.sources, source)
	}
}

// WithEnvSource 追加一个按前缀过滤的环境变量配置源。
func WithEnvSource(prefix string, opts ...EnvSourceOption) ProviderOption {
	return func(p *Provider) {
		p.sources = append(p.sources, NewEnvSource(prefix, opts...))
	}
}

// WithYAMLSource 追加一个 YAML 文件配置源。
func WithYAMLSource(path string) ProviderOption {
	return func(p *Provider) {
		p.sources = append(p.sources, NewYAMLSource(path))
	}
}

// WithDotEnvSource 追加一个 .env 文件配置源。
func WithDotEnvSource(path string, opts ...DotEnvSourceOption) ProviderOption {
	return func(p *Provider) {
		p.sources = append(p.sources, NewDotEnvSource(path, opts...))
	}
}

// WithDefaults 追加一个默认值配置源。
func WithDefaults(defaults map[string]any) ProviderOption {
	return func(p *Provider) {
		p.sources = append(p.sources, NewDefaultsSource(defaults))
	}
}

// WithWarningHandler registers a handler that will be called when tolerant
// getters fall back to defaults due to conversion failures.
//
// If not set, warnings are still recorded and can be retrieved via Warnings().
func WithWarningHandler(fn func(ConfigWarning)) ProviderOption {
	return func(p *Provider) {
		p.onWarning = fn
	}
}

// WithWarningBufferSize sets the maximum number of tolerant-get warnings kept
// in memory. When full, the oldest warning is discarded.
//
// size <= 0 disables in-memory warning recording.
func WithWarningBufferSize(size int) ProviderOption {
	return func(p *Provider) {
		p.maxWarns = size
	}
}
